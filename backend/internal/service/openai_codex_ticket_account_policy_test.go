package service

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketAccountPolicy(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
			for _, flag := range []any{nil, false, true, "true", "false", 1} {
				t.Run(fmt.Sprintf("global=%t/type=%s/flag=%v", global, accountType, flag), func(t *testing.T) {
					account := ticketTestAccount(41)
					account.Extra = nil
					account.Type, account.Status = accountType, StatusActive
					if flag != nil {
						account.Extra = map[string]any{openAICodexTicketEnabledExtraKey: flag}
					}
					active := global && flag == true
					cfg := config.OpenAICodexTicketConfig{Enabled: global, FailClosed: true,
						Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://proxy.example.com:8080"}
					var probes atomic.Int64
					upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
						probes.Add(1)
						return codexTicketResponse(), nil
					}}
					svc := ticketTestService(t, cfg, upstream)
					svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
					ctx := context.Background()
					require.Equal(t, active, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-6-astra", false))
					h := http.Header{}
					err := svc.applyOpenAICodexTicket(ctx, account, "gpt-6-astra", h)
					if active {
						require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
					} else {
						require.NoError(t, err)
					}
					// Both the refresh loop and a direct probe must honor both switches.
					svc.refreshOpenAICodexTickets(ctx)
					svc.probeOnceOpenAICodexTicket(ctx, account, "gpt-6-astra")
					if active {
						require.Equal(t, int64(2), probes.Load())
					} else {
						require.Zero(t, probes.Load())
					}
					// A saved ticket from before disabling must not be injected or displayed.
					ticket := &openAICodexTicket{State: fakeCodexTicketState(292), Length: 292,
						Model: "gpt-6-astra", ExpiresAt: time.Now().Add(time.Hour)}
					if account.Extra == nil {
						account.Extra = make(map[string]any)
					}
					account.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
					h.Set(openAICodexTurnStateHeader, "existing-client-state")
					require.NoError(t, svc.applyOpenAICodexTicket(ctx, account, ticket.Model, h))
					statuses := OpenAICodexTicketStatuses(account, cfg, time.Now())
					if active {
						require.Equal(t, ticket.State, h.Get(openAICodexTurnStateHeader))
						require.Len(t, statuses, 1)
						require.True(t, statuses[0].Ready)
					} else {
						require.Equal(t, "existing-client-state", h.Get(openAICodexTurnStateHeader))
						require.Empty(t, statuses)
					}
				})
			}
		}
	}
}

func TestCodexTicketAccountPolicyUpdatesPreserveExclusion(t *testing.T) {
	account := ticketTestAccount(41)
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	for _, step := range []struct {
		extra map[string]any
		want  bool
	}{
		{map[string]any{openAICodexTicketEnabledExtraKey: false}, false},
		{map[string]any{"custom": true}, false},
		{nil, false},
		{map[string]any{openAICodexTicketEnabledExtraKey: true}, true},
	} {
		updated, err := svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: step.extra})
		require.NoError(t, err)
		require.Equal(t, step.want, isOpenAICodexTicketAccount(updated))
	}
}

func TestCodexTicketAccountPolicyRejectsInvalidWrites(t *testing.T) {
	for _, value := range []any{"false", 0, nil, []any{false}} {
		extra := map[string]any{openAICodexTicketEnabledExtraKey: value}
		_, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, extra)
		require.Error(t, err)
		// Invalid inputs must fail before repository writes on every admin entry point.
		svc := &adminServiceImpl{}
		_, err = svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: extra})
		require.Error(t, err)
		require.Error(t, svc.UpdateAccountExtra(context.Background(), 41, extra))
		_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{41}, Extra: extra})
		require.Error(t, err)
	}
}
