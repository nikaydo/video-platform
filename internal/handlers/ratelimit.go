package handlers

import (
	"strconv"
	"sync"
	"time"
)

// limiter — скользящее окно, ограничивающее число запросов от одного
// источника.
//
// Применяется к эндпоинтам регистрации и входа: без него можно было бы
// перебирать пароли или плодить аккаунты. Состояние хранится в памяти:
// сброс счётчиков при перезапуске процесса безопасен.
type limiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	entries map[string]*window
	lastGC  time.Time
}

// window — счётчик запросов для одного источника.
type window struct {
	count int
	// started — начало текущего окна.
	started time.Time
}

// newLimiter создаёт ограничитель.
func newLimiter(limit int, w time.Duration) *limiter {
	if limit < 1 {
		limit = 1
	}
	if w <= 0 {
		w = time.Minute
	}
	return &limiter{
		limit:   limit,
		window:  w,
		entries: make(map[string]*window),
		lastGC:  time.Now(),
	}
}

// maxLimiterEntries ограничивает число отслеживаемых источников.
//
// Без предела перебором адресов можно было бы раздуть память процесса.
const maxLimiterEntries = 10000

// allow сообщает, можно ли выполнить запрос от указанного источника.
func (l *limiter) allow(key string) (bool, time.Duration) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.gcLocked(now)

	entry, ok := l.entries[key]
	if !ok {
		l.entries[key] = &window{count: 1, started: now}
		return true, 0
	}

	if now.Sub(entry.started) >= l.window {
		entry.count = 1
		entry.started = now
		return true, 0
	}

	if entry.count >= l.limit {
		return false, l.window - now.Sub(entry.started)
	}

	entry.count++
	return true, 0
}

// gcLocked удаляет истёкшие окна. Вызывается под блокировкой.
func (l *limiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < l.window {
		return
	}
	l.lastGC = now

	for key, entry := range l.entries {
		if now.Sub(entry.started) >= l.window {
			delete(l.entries, key)
		}
	}

	// Если записей всё равно много, карта сбрасывается целиком: точность
	// счётчиков не настолько важна, сколько важно не терять память.
	if len(l.entries) > maxLimiterEntries {
		l.entries = make(map[string]*window)
	}
}

// formatSeconds округляет интервал до целых секунд с положительным минимумом.
func formatSeconds(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}
