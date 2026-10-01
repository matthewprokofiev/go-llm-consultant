package storage

import (
	"context"
	"fmt"
)

// Виды событий статистики. Ни одно не несёт текста, имени, телефона или id
// пользователя: по таблице видно, сколько и чего было, но не кто и что писал.
const (
	EventLLMError       = "llm_error"
	EventLimited        = "limited"
	EventLeadSent       = "lead_sent"
	EventLeadFailed     = "lead_failed"
	EventQuestionSent   = "question_sent"
	EventQuestionFailed = "question_failed"
)

// Record пишет одно событие. Вызывающий трактует ошибку как некритичную: статистика
// вторична по отношению к ответу человеку.
func (s *Storage) Record(ctx context.Context, kind string, inputTokens, outputTokens int) error {
	const query = `INSERT INTO events (kind, input_tokens, output_tokens) VALUES ($1, $2, $3)`
	if _, err := s.pool.Exec(ctx, query, kind, inputTokens, outputTokens); err != nil {
		return fmt.Errorf("запись события: %w", err)
	}
	return nil
}

// Summary — сводка за последние days дней.
type Summary struct {
	Counts       map[string]int
	InputTokens  int
	OutputTokens int
}

func (s *Storage) Summary(ctx context.Context, days int) (Summary, error) {
	const query = `
		SELECT kind, count(*), coalesce(sum(input_tokens), 0), coalesce(sum(output_tokens), 0)
		FROM events
		WHERE created_at >= now() - make_interval(days => $1)
		GROUP BY kind`

	rows, err := s.pool.Query(ctx, query, days)
	if err != nil {
		return Summary{}, fmt.Errorf("сводка событий: %w", err)
	}
	defer rows.Close()

	sum := Summary{Counts: map[string]int{}}
	for rows.Next() {
		var kind string
		var count, in, out int
		if err := rows.Scan(&kind, &count, &in, &out); err != nil {
			return Summary{}, fmt.Errorf("разбор сводки: %w", err)
		}
		sum.Counts[kind] = count
		sum.InputTokens += in
		sum.OutputTokens += out
	}
	return sum, rows.Err()
}
