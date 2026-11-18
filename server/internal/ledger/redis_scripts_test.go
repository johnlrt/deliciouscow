package ledger

import (
	"reflect"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestLuaScriptsNoTonumberOnMoney(t *testing.T) {
	scripts := map[string]*redis.Script{
		"LuaDepositScript":         LuaDepositScript,
		"LuaReserveScript":         LuaReserveScript,
		"LuaRefundScript":          LuaRefundScript,
		"LuaSettleScript":          LuaSettleScript,
		"LuaMatureScript":          LuaMatureScript,
		"LuaReservePayoutScript":   LuaReservePayoutScript,
		"LuaBroadcastPayoutScript": LuaBroadcastPayoutScript,
	}
	for name, s := range scripts {
		if s == nil {
			t.Fatalf("%s is nil", name)
		}
		// Inspect unexported src field of *redis.Script to guard against accidental tonumber() regression.
		v := reflect.ValueOf(s).Elem().FieldByName("src")
		if !v.IsValid() {
			t.Fatalf("%s missing src field", name)
		}
		src := v.String()
		if strings.Contains(src, "tonumber") {
			t.Fatalf("%s must not use Lua tonumber() on money values", name)
		}
		if !strings.Contains(src, "dec_cmp") || !strings.Contains(src, "check_key_type") {
			t.Fatalf("%s must use decimal-string arithmetic and key-type prevalidation", name)
		}
	}
}
