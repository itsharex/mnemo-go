package app

import (
	"context"
	"testing"
	"time"

	"mnemo-go/internal/model"
)

func TestAccountKeepAliveDelaysStayWithinConfiguredRange(t *testing.T) {
	for range 100 {
		initial := accountKeepAliveInitialDelay()
		if initial < accountKeepAliveInitialMin || initial > accountKeepAliveInitialMax {
			t.Fatalf("初始保活延迟 = %v，超出范围", initial)
		}
		next := accountKeepAliveNextDelay()
		min := accountKeepAliveInterval - accountKeepAliveJitter
		max := accountKeepAliveInterval + accountKeepAliveJitter
		if next < min || next > max {
			t.Fatalf("下一轮保活延迟 = %v，超出范围", next)
		}
	}
}

func TestWaitAccountKeepAliveHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if waitAccountKeepAlive(ctx, time.Hour) {
		t.Fatal("已取消的保活等待不应继续")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("取消后的保活等待耗时过长: %v", elapsed)
	}
}

func TestShouldKeepAccountAlive(t *testing.T) {
	if shouldKeepAccountAlive(nil) {
		t.Fatal("空账号不应参与保活")
	}
	if shouldKeepAccountAlive(&model.Account{UserID: "a"}) {
		t.Fatal("没有令牌的账号不应参与保活")
	}
	if shouldKeepAccountAlive(&model.Account{UserID: "a", Disabled: true, Token: &model.TokenInfo{}}) {
		t.Fatal("已停用账号不应参与保活")
	}
	if !shouldKeepAccountAlive(&model.Account{UserID: "a", Token: &model.TokenInfo{}}) {
		t.Fatal("有效账号应参与保活")
	}
}
