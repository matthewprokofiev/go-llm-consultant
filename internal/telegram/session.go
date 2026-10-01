package telegram

import (
	"context"
	"sync"
	"time"

	"github.com/matthewprokofiev/go-llm-consultant/internal/consultant"
)

const (
	// historyTurns — сколько последних пар «вопрос–ответ» модель видит для связности.
	historyTurns = 3
	// sessionIdle — после такой паузы разговор начинается заново: история, начатая
	// заявка и признак «заявку уже предлагали» забываются.
	sessionIdle = 30 * time.Minute
	// rateWindow — окно лимитов на вопросы и заявки.
	rateWindow = time.Hour
)

// session — всё, что бот помнит о чате. Живёт только в памяти и пропадает при
// перезапуске или долгой паузе: на диск и в лог отсюда не попадает ничего.
type session struct {
	mu sync.Mutex

	lastSeen   time.Time
	history    []consultant.Turn
	offerShown bool
	form       *leadForm

	// Вопрос, под которым висят кнопки: «Передать вопрос» или «Оставить заявку».
	pendingQuestion string
	pendingContext  string
	pendingInterest string

	questions []time.Time
	leads     []time.Time
}

func (s *session) resetConversation() {
	s.history = nil
	s.offerShown = false
	s.form = nil
	s.pendingQuestion, s.pendingContext, s.pendingInterest = "", "", ""
}

func (s *session) remember(question, answer string) {
	s.history = append(s.history, consultant.Turn{Question: question, Answer: answer})
	if len(s.history) > historyTurns {
		s.history = s.history[len(s.history)-historyTurns:]
	}
}

// previousQuestion — вопрос перед последним: короткий контекст для владельца.
func (s *session) previousQuestion() string {
	if len(s.history) < 2 {
		return ""
	}
	return s.history[len(s.history)-2].Question
}

// withinLimit выбрасывает отметки старше часа и проверяет, осталось ли место.
// limit 0 — без ограничения.
func withinLimit(times *[]time.Time, limit int, now time.Time) bool {
	kept := (*times)[:0]
	for _, t := range *times {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	*times = kept
	return limit == 0 || len(kept) < limit
}

type sessions struct {
	mu     sync.Mutex
	byChat map[int64]*session
	now    func() time.Time
}

func newSessions() *sessions {
	return &sessions{byChat: map[int64]*session{}, now: time.Now}
}

// acquire возвращает сессию чата под её мьютексом — вызывающий обязан отпустить
// s.mu. Лок на чат держится всю обработку сообщения: два быстрых сообщения одного
// человека обрабатываются по очереди и не путают шаги заявки.
func (ss *sessions) acquire(chatID int64) *session {
	ss.mu.Lock()
	s, ok := ss.byChat[chatID]
	if !ok {
		s = &session{}
		ss.byChat[chatID] = s
	}
	ss.mu.Unlock()

	s.mu.Lock()
	now := ss.now()
	if now.Sub(s.lastSeen) > sessionIdle {
		s.resetConversation()
	}
	s.lastSeen = now
	return s
}

// cleanup забывает чаты, молчащие дольше окна лимитов. Занятые сессии (идёт ответ)
// пропускаются: TryLock не даёт уборке ждать чужой запрос к нейросети.
func (ss *sessions) cleanup() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := ss.now()
	for id, s := range ss.byChat {
		if !s.mu.TryLock() {
			continue
		}
		idle := now.Sub(s.lastSeen) > rateWindow
		s.mu.Unlock()
		if idle {
			delete(ss.byChat, id)
		}
	}
}

func (ss *sessions) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ss.cleanup()
		}
	}
}
