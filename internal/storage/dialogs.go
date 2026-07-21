package storage

import (
	"context"
	"fmt"
)

// Dialog — одна пара вопрос/ответ для журнала. TokensUsed — оценка (интерфейс
// LLMClient намеренно узкий и точный usage не отдаёт), поэтому поле необязательное.
type Dialog struct {
	UserTgID   int64
	Question   string
	Answer     string
	Provider   string
	TokensUsed int
}

// SaveDialog пишет диалог в журнал. Логирование не должно ронять ответ пользователю:
// вызывающий трактует ошибку как некритичную (пишет в лог и продолжает).
func (s *Storage) SaveDialog(ctx context.Context, d Dialog) error {
	const query = `
		INSERT INTO dialogs (user_tg_id, question, answer, provider, tokens_used)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := s.pool.Exec(ctx, query, d.UserTgID, d.Question, d.Answer, d.Provider, d.TokensUsed)
	if err != nil {
		return fmt.Errorf("сохранение диалога: %w", err)
	}
	return nil
}
