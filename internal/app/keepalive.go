package app

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
)

const (
	// 保活频率刻意保持低频：服务商令牌刷新通常不需要高频调用，而频繁
	// 请求反而容易触发限流或风控。
	accountKeepAliveInterval   = 12 * time.Hour
	accountKeepAliveJitter     = time.Hour
	accountKeepAliveInitialMin = 2 * time.Minute
	accountKeepAliveInitialMax = 12 * time.Minute
	accountKeepAliveTimeout    = 90 * time.Second
	accountKeepAliveWorkers    = 2
)

// startAccountKeepAlive 在应用存活期间启动一次低频会话保活。应用完全退出
// 后无法维持第三方会话，这是桌面应用和服务商认证机制共同决定的限制。
func (a *App) startAccountKeepAlive() {
	if a == nil {
		return
	}
	ctx, cancel := context.WithCancel(a.appContext())
	a.stateMu.Lock()
	previous := a.keepAliveCancel
	a.keepAliveCancel = cancel
	a.stateMu.Unlock()
	if previous != nil {
		previous()
	}
	go a.runAccountKeepAlive(ctx)
}

func (a *App) runAccountKeepAlive(ctx context.Context) {
	if !waitAccountKeepAlive(ctx, accountKeepAliveInitialDelay()) {
		return
	}
	for {
		a.refreshAllAccountSessions(ctx)
		if !waitAccountKeepAlive(ctx, accountKeepAliveNextDelay()) {
			return
		}
	}
}

func waitAccountKeepAlive(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func accountKeepAliveInitialDelay() time.Duration {
	span := accountKeepAliveInitialMax - accountKeepAliveInitialMin
	return accountKeepAliveInitialMin + time.Duration(rand.Int63n(int64(span)+1))
}

func accountKeepAliveNextDelay() time.Duration {
	// 每轮加入正负一小时抖动，使多账号、多个客户端不会在固定时刻集中
	// 请求同一服务商。
	return accountKeepAliveInterval + time.Duration(rand.Int63n(int64(accountKeepAliveJitter)*2+1)) - accountKeepAliveJitter
}

// refreshAllAccountSessions 对全部已登录且启用的账号执行一次会话刷新。
// 并发限制为两个，兼顾启动后多个账号的耗时和服务商的限流风险。
func (a *App) refreshAllAccountSessions(parent context.Context) {
	st, err := a.storeOrError()
	if err != nil {
		logging.Warn("account keep-alive skipped: store unavailable", "error", err)
		return
	}
	accounts, err := st.ListAccounts()
	if err != nil {
		logging.Warn("account keep-alive skipped: account list unavailable", "error", err)
		return
	}

	workers := make(chan struct{}, accountKeepAliveWorkers)
	var wg sync.WaitGroup
	for _, account := range accounts {
		if !shouldKeepAccountAlive(account) {
			continue
		}
		userID := account.UserID
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case workers <- struct{}{}:
			case <-parent.Done():
				return
			}
			defer func() { <-workers }()

			ctx, cancel := context.WithTimeout(parent, accountKeepAliveTimeout)
			defer cancel()
			if _, refreshErr := a.refreshAccount(ctx, userID, true); refreshErr != nil {
				logging.Warn("account keep-alive failed", "account_id", redactID(userID), "error", refreshErr)
				return
			}
			logging.Debug("account keep-alive completed", "account_id", redactID(userID))
		}()
	}
	wg.Wait()
}

func shouldKeepAccountAlive(account *model.Account) bool {
	return account != nil && !account.Disabled && account.UserID != "" && account.Token != nil
}
