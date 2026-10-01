package telegram

import (
	"testing"
	"time"
)

func TestWithinLimit(t *testing.T) {
	now := time.Now()
	times := []time.Time{now.Add(-2 * time.Hour), now.Add(-30 * time.Minute), now.Add(-time.Minute)}

	if withinLimit(&times, 2, now) {
		t.Error("две отметки за час при лимите 2 — лимит исчерпан")
	}
	if len(times) != 2 {
		t.Errorf("отметка старше часа должна выпасть, осталось %d", len(times))
	}
	if !withinLimit(&times, 3, now) {
		t.Error("при лимите 3 место ещё есть")
	}
	if !withinLimit(&times, 0, now) {
		t.Error("лимит 0 — без ограничения")
	}
}

func TestSessionResetsAfterIdle(t *testing.T) {
	ss := newSessions()
	now := time.Now()
	ss.now = func() time.Time { return now }

	s := ss.acquire(1)
	for _, q := range []string{"1", "2", "3", "4"} {
		s.remember(q, "ответ")
	}
	s.offerShown = true
	s.form = &leadForm{}
	s.leads = []time.Time{now}
	s.mu.Unlock()

	if len(s.history) != historyTurns || s.history[0].Question != "2" {
		t.Errorf("история = %+v, ожидались последние %d пары", s.history, historyTurns)
	}

	now = now.Add(sessionIdle + time.Second)
	s = ss.acquire(1)
	defer s.mu.Unlock()
	if s.history != nil || s.offerShown || s.form != nil {
		t.Error("после получаса тишины разговор должен начаться заново")
	}
	if len(s.leads) != 1 {
		t.Error("лимиты переживают сброс разговора, иначе их обойти паузой")
	}
}

func TestSessionsCleanup(t *testing.T) {
	ss := newSessions()
	now := time.Now()
	ss.now = func() time.Time { return now }

	ss.acquire(1).mu.Unlock()
	busy := ss.acquire(2) // занята: идёт ответ

	now = now.Add(rateWindow + time.Minute)
	ss.cleanup()
	busy.mu.Unlock()

	if _, ok := ss.byChat[1]; ok {
		t.Error("молчащий дольше часа чат должен быть забыт")
	}
	if _, ok := ss.byChat[2]; !ok {
		t.Error("занятую сессию уборка трогать не должна")
	}
}
