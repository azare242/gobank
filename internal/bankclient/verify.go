package bankclient

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
)

// VerifyBalances checks all expected account balances and their sum after the
// caller has reconciled every operation. Each account uses its owner's JWT.
func VerifyBalances(ctx context.Context, gateway string, tokens map[string]string, expected map[string]int64) (*big.Int, error) {
	total := new(big.Int)
	for account, want := range expected {
		token := tokens[account]
		if token == "" {
			return nil, fmt.Errorf("missing owner token for %s", account)
		}
		client, e := New(gateway, token)
		if e != nil {
			return nil, e
		}
		if len(client.Servers) != 1 {
			return nil, fmt.Errorf("verification requires one Gateway URL")
		}
		reply, e := client.Do(ctx, "GET", "/accounts/"+url.PathEscape(account)+"/balance", "", nil)
		if e != nil {
			return nil, e
		}
		if reply.HTTP != 200 || reply.Balance < 0 || reply.Balance != want {
			return nil, fmt.Errorf("account %s: expected %d, received %d (%s)", account, want, reply.Balance, reply.Error)
		}
		total.Add(total, big.NewInt(reply.Balance))
	}
	return total, nil
}
