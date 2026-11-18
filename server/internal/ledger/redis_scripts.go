package ledger

import "github.com/redis/go-redis/v9"

const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// luaDecimalHelpers provides pure base-10 decimal-string arithmetic and key-type prevalidation
// for Redis Lua 5.1. Money values NEVER pass through tonumber() or IEEE-754 floats,
// preserving exact 1-piconero precision above 2^53 up to 9,000,000,000,000,000,000 (C1/C5).
const luaDecimalHelpers = `
local MAX_MONEY = "9000000000000000000"
local GENESIS_HASH = "` + genesisHash + `"

local function is_valid_money(s)
  if type(s) ~= "string" then return false end
  if s == "0" then return true end
  if not string.match(s, "^[1-9][0-9]*$") then return false end
  if #s > #MAX_MONEY then return false end
  if #s == #MAX_MONEY and s > MAX_MONEY then return false end
  return true
end

local function dec_cmp(a, b)
  if #a ~= #b then
    if #a < #b then return -1 else return 1 end
  end
  if a == b then return 0 end
  if a < b then return -1 else return 1 end
end

local function strip_leading_zeros(s)
  local stripped = string.match(s, "^0*(.+)$")
  if not stripped or stripped == "" then return "0" end
  return stripped
end

local function dec_add(a, b)
  local i = #a
  local j = #b
  local carry = 0
  local digits = {}
  local k = 0
  while i > 0 or j > 0 or carry > 0 do
    local da = 0
    if i > 0 then
      da = string.byte(a, i) - 48
      i = i - 1
    end
    local db = 0
    if j > 0 then
      db = string.byte(b, j) - 48
      j = j - 1
    end
    local sum = da + db + carry
    if sum >= 10 then
      carry = 1
      sum = sum - 10
    else
      carry = 0
    end
    k = k + 1
    digits[k] = string.char(48 + sum)
  end
  local rev = {}
  for idx = 1, k do
    rev[idx] = digits[k - idx + 1]
  end
  local res = strip_leading_zeros(table.concat(rev))
  if dec_cmp(res, MAX_MONEY) > 0 then
    return nil, "overflow"
  end
  return res, nil
end

local function dec_sub(a, b)
  if dec_cmp(a, b) < 0 then
    return nil, "underflow"
  end
  local i = #a
  local j = #b
  local borrow = 0
  local digits = {}
  local k = 0
  while i > 0 do
    local da = string.byte(a, i) - 48 - borrow
    i = i - 1
    local db = 0
    if j > 0 then
      db = string.byte(b, j) - 48
      j = j - 1
    end
    if da < db then
      da = da + 10
      borrow = 1
    else
      borrow = 0
    end
    k = k + 1
    digits[k] = string.char(48 + (da - db))
  end
  local rev = {}
  for idx = 1, k do
    rev[idx] = digits[k - idx + 1]
  end
  return strip_leading_zeros(table.concat(rev)), nil
end

local function check_key_type(key, expected)
  local t = redis.call("TYPE", key).ok
  if t ~= "none" and t ~= expected then
    return false
  end
  return true
end

local function hget_money(key, field)
  local v = redis.call("HGET", key, field)
  if not v then return "0", true end
  if not is_valid_money(v) then return nil, false end
  return v, true
end

local function bump_rev(key)
  local r = redis.call("HGET", key, "revision")
  if not r or not string.match(r, "^[0-9]+$") then
    return "1"
  end
  local nxt, err = dec_add(r, "1")
  if err then return "1" end
  return nxt
end
`

// LuaDepositScript atomically credits user.available + platform cash_hot, appends ledger & outbox, and records tombstone.
// KEYS: [1]=opKey, [2]=balKey, [3]=platKey, [4]=ledgerKey, [5]=headKey, [6]=accLedgerKey, [7]=outboxKey
// ARGV: [1]=amount, [2]=opID, [3]=refID, [4]=now, [5]=expectedPrevHash, [6]=entryHash, [7]=ledgerID, [8]=postingsJSON, [9]=accLedgerRowJSON
var LuaDepositScript = redis.NewScript(luaDecimalHelpers + `
local opKey, balKey, platKey, ledgerKey, headKey, accLedgerKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5], KEYS[6], KEYS[7]
local amount, opID, refID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON, accRowJSON = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8], ARGV[9]

if not check_key_type(opKey, "hash") or not check_key_type(balKey, "hash") or not check_key_type(platKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(accLedgerKey, "list") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(amount) or amount == "0" then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local curAvail, ok1 = hget_money(balKey, "available")
local curCash, ok2 = hget_money(platKey, "cash_hot")
if not ok1 or not ok2 then return {-3, "corrupt_balance"} end

local newAvail, err1 = dec_add(curAvail, amount)
local newCash, err2 = dec_add(curCash, amount)
if err1 or err2 then return {-1, "overflow"} end

local balRev = bump_rev(balKey)
local platRev = bump_rev(platKey)

redis.call("HSET", balKey, "available", newAvail, "revision", balRev, "updated_at", now)
redis.call("HSET", platKey, "cash_hot", newCash, "revision", platRev, "updated_at", now)
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "deposit", "ref_id", refID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
if accRowJSON ~= "" then
  local row = string.gsub(accRowJSON, "__BALANCE_AFTER__", newAvail)
  redis.call("RPUSH", accLedgerKey, row)
end
redis.call("HSET", opKey, "op_id", opID, "kind", "deposit", "success", "1", "amount", amount, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "deposit", "amount", amount, "ts", now)
return {1, "ok"}
`)

// LuaReserveScript atomically checks available balance + daily spend cap, moves available -> reserved.
// KEYS: [1]=opKey, [2]=balKey, [3]=keySpendKey (may be dummy if apiKeyID==""), [4]=ledgerKey, [5]=headKey, [6]=outboxKey
// ARGV: [1]=amount, [2]=opID, [3]=reqID, [4]=attemptID, [5]=now, [6]=expectedPrevHash, [7]=entryHash, [8]=ledgerID, [9]=postingsJSON, [10]=hasKeyCap ("1"/"0"), [11]=dailyCap, [12]=hasAPIKey ("1"/"0")
var LuaReserveScript = redis.NewScript(luaDecimalHelpers + `
local opKey, balKey, keySpendKey, ledgerKey, headKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5], KEYS[6]
local amount, opID, reqID, attemptID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON, hasKeyCap, dailyCap, hasAPIKey = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8], ARGV[9], ARGV[10], ARGV[11], ARGV[12]

if not check_key_type(opKey, "hash") or not check_key_type(balKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if hasAPIKey == "1" and not check_key_type(keySpendKey, "string") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(amount) then
  return {-1, "invalid_amount"}
end
if hasKeyCap == "1" and not is_valid_money(dailyCap) then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local curAvail, ok1 = hget_money(balKey, "available")
local curRes, ok2 = hget_money(balKey, "reserved")
if not ok1 or not ok2 then return {-3, "corrupt_balance"} end

if dec_cmp(curAvail, amount) < 0 then
  return {-1, "insufficient_funds"}
end
local newAvail, err1 = dec_sub(curAvail, amount)
local newRes, err2 = dec_add(curRes, amount)
if err1 or err2 then return {-1, "overflow"} end

local newKeySpend = "0"
if hasAPIKey == "1" then
  local curSpend = redis.call("GET", keySpendKey)
  if not curSpend then curSpend = "0" end
  if not is_valid_money(curSpend) then return {-3, "corrupt_balance"} end
  local err3
  newKeySpend, err3 = dec_add(curSpend, amount)
  if err3 then return {-1, "overflow"} end
  if hasKeyCap == "1" and dec_cmp(newKeySpend, dailyCap) > 0 then
    return {-1, "spend_cap_exceeded"}
  end
end

local balRev = bump_rev(balKey)
redis.call("HSET", balKey, "available", newAvail, "reserved", newRes, "revision", balRev, "updated_at", now)
if hasAPIKey == "1" then
  redis.call("SET", keySpendKey, newKeySpend, "EX", 2764800)
end
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "reserve", "req_id", reqID, "attempt_id", attemptID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
redis.call("HSET", opKey, "op_id", opID, "kind", "reserve", "success", "1", "amount", amount, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "reserve", "req_id", reqID, "amount", amount, "ts", now)
return {1, "ok"}
`)

// LuaRefundScript atomically moves reserved -> available and releases key-spend if applicable.
// KEYS: [1]=opKey, [2]=balKey, [3]=keySpendKey, [4]=ledgerKey, [5]=headKey, [6]=outboxKey
// ARGV: [1]=amount, [2]=opID, [3]=reqID, [4]=attemptID, [5]=now, [6]=expectedPrevHash, [7]=entryHash, [8]=ledgerID, [9]=postingsJSON, [10]=hasAPIKey ("1"/"0")
var LuaRefundScript = redis.NewScript(luaDecimalHelpers + `
local opKey, balKey, keySpendKey, ledgerKey, headKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5], KEYS[6]
local amount, opID, reqID, attemptID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON, hasAPIKey = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8], ARGV[9], ARGV[10]

if not check_key_type(opKey, "hash") or not check_key_type(balKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if hasAPIKey == "1" and not check_key_type(keySpendKey, "string") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(amount) then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local curRes, ok1 = hget_money(balKey, "reserved")
local curAvail, ok2 = hget_money(balKey, "available")
if not ok1 or not ok2 then return {-3, "corrupt_balance"} end

if dec_cmp(curRes, amount) < 0 then
  return {-1, "insufficient_reserved"}
end
local newRes, err1 = dec_sub(curRes, amount)
local newAvail, err2 = dec_add(curAvail, amount)
if err1 or err2 then return {-1, "overflow"} end

local newKeySpend = nil
if hasAPIKey == "1" then
  local curSpend = redis.call("GET", keySpendKey)
  if curSpend and is_valid_money(curSpend) then
    if dec_cmp(curSpend, amount) >= 0 then
      newKeySpend = dec_sub(curSpend, amount)
    else
      newKeySpend = "0"
    end
  end
end

local balRev = bump_rev(balKey)
redis.call("HSET", balKey, "reserved", newRes, "available", newAvail, "revision", balRev, "updated_at", now)
if newKeySpend ~= nil then
  redis.call("SET", keySpendKey, newKeySpend, "EX", 2764800)
end
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "refund", "req_id", reqID, "attempt_id", attemptID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
redis.call("HSET", opKey, "op_id", opID, "kind", "refund", "success", "1", "amount", amount, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "refund", "req_id", reqID, "amount", amount, "ts", now)
return {1, "ok"}
`)

// LuaSettleScript atomically settles reservation -> user refund + owner pending + platform equity.
// KEYS: [1]=opKey, [2]=userBalKey, [3]=ownerBalKey, [4]=platKey, [5]=keySpendKey, [6]=ledgerKey, [7]=headKey, [8]=userAccLedgerKey, [9]=ownerAccLedgerKey, [10]=outboxKey
// ARGV: [1]=reserved, [2]=actual, [3]=ownerCredit, [4]=opID, [5]=reqID, [6]=attemptID, [7]=now, [8]=expectedPrevHash, [9]=entryHash, [10]=ledgerID, [11]=postingsJSON, [12]=userRowJSON, [13]=ownerRowJSON, [14]=hasAPIKey, [15]=sameAccount ("1"/"0")
var LuaSettleScript = redis.NewScript(luaDecimalHelpers + `
local opKey, userBalKey, ownerBalKey, platKey, keySpendKey, ledgerKey, headKey, userAccLedgerKey, ownerAccLedgerKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5], KEYS[6], KEYS[7], KEYS[8], KEYS[9], KEYS[10]
local reserved, actual, ownerCredit, opID, reqID, attemptID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON, userRowJSON, ownerRowJSON, hasAPIKey, sameAccount = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8], ARGV[9], ARGV[10], ARGV[11], ARGV[12], ARGV[13], ARGV[14], ARGV[15]

if not check_key_type(opKey, "hash") or not check_key_type(userBalKey, "hash") or not check_key_type(ownerBalKey, "hash") or not check_key_type(platKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(userAccLedgerKey, "list") or not check_key_type(ownerAccLedgerKey, "list") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if hasAPIKey == "1" and not check_key_type(keySpendKey, "string") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(reserved) or not is_valid_money(actual) or not is_valid_money(ownerCredit) then
  return {-1, "invalid_amount"}
end
if dec_cmp(actual, reserved) > 0 or dec_cmp(ownerCredit, actual) > 0 then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local refund, _ = dec_sub(reserved, actual)
local platformTake, _ = dec_sub(actual, ownerCredit)

local curUserRes, ok1 = hget_money(userBalKey, "reserved")
local curUserAvail, ok2 = hget_money(userBalKey, "available")
local curOwnerPending, ok3 = hget_money(ownerBalKey, "earnings_pending")
local curEquity, ok4 = hget_money(platKey, "equity")
if not ok1 or not ok2 or not ok3 or not ok4 then return {-3, "corrupt_balance"} end

if dec_cmp(curUserRes, reserved) < 0 then
  return {-1, "insufficient_reserved"}
end
local newUserRes, err1 = dec_sub(curUserRes, reserved)
local newUserAvail, err2 = dec_add(curUserAvail, refund)
local newOwnerPending, err3 = dec_add(curOwnerPending, ownerCredit)
local newEquity, err4 = dec_add(curEquity, platformTake)
if err1 or err2 or err3 or err4 then return {-1, "overflow"} end

local newKeySpend = nil
if hasAPIKey == "1" then
  local curSpend = redis.call("GET", keySpendKey)
  if not curSpend then curSpend = "0" end
  if not is_valid_money(curSpend) then return {-3, "corrupt_balance"} end
  local baseSpend = "0"
  if dec_cmp(curSpend, reserved) >= 0 then
    baseSpend = dec_sub(curSpend, reserved)
  end
  local err5
  newKeySpend, err5 = dec_add(baseSpend, actual)
  if err5 then return {-1, "overflow"} end
end

local uRev = bump_rev(userBalKey)
redis.call("HSET", userBalKey, "reserved", newUserRes, "available", newUserAvail, "revision", uRev, "updated_at", now)
if sameAccount == "1" then
  redis.call("HSET", ownerBalKey, "earnings_pending", newOwnerPending, "updated_at", now)
else
  local oRev = bump_rev(ownerBalKey)
  redis.call("HSET", ownerBalKey, "earnings_pending", newOwnerPending, "revision", oRev, "updated_at", now)
end
local pRev = bump_rev(platKey)
redis.call("HSET", platKey, "equity", newEquity, "revision", pRev, "updated_at", now)
if newKeySpend ~= nil then
  redis.call("SET", keySpendKey, newKeySpend, "EX", 2764800)
end
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "settle", "req_id", reqID, "attempt_id", attemptID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
if userRowJSON ~= "" then
  local r1 = string.gsub(userRowJSON, "__BALANCE_AFTER__", newUserAvail)
  redis.call("RPUSH", userAccLedgerKey, r1)
end
if ownerRowJSON ~= "" then
  local r2 = string.gsub(ownerRowJSON, "__BALANCE_AFTER__", newOwnerPending)
  redis.call("RPUSH", ownerAccLedgerKey, r2)
end
redis.call("HSET", opKey, "op_id", opID, "kind", "settle", "success", "1", "amount", actual, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "settle", "req_id", reqID, "amount", actual, "ts", now)
return {1, "ok"}
`)

// LuaMatureScript atomically moves owner earnings_pending -> earnings_available.
// KEYS: [1]=opKey, [2]=balKey, [3]=ledgerKey, [4]=headKey, [5]=outboxKey
// ARGV: [1]=amount, [2]=opID, [3]=ownerID, [4]=now, [5]=expectedPrevHash, [6]=entryHash, [7]=ledgerID, [8]=postingsJSON
var LuaMatureScript = redis.NewScript(luaDecimalHelpers + `
local opKey, balKey, ledgerKey, headKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5]
local amount, opID, ownerID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8]

if not check_key_type(opKey, "hash") or not check_key_type(balKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(amount) or amount == "0" then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local curPending, ok1 = hget_money(balKey, "earnings_pending")
local curAvail, ok2 = hget_money(balKey, "earnings_available")
if not ok1 or not ok2 then return {-3, "corrupt_balance"} end

if dec_cmp(curPending, amount) < 0 then
  return {-1, "invalid_amount"}
end
local newPending, err1 = dec_sub(curPending, amount)
local newAvail, err2 = dec_add(curAvail, amount)
if err1 or err2 then return {-1, "overflow"} end

local balRev = bump_rev(balKey)
redis.call("HSET", balKey, "earnings_pending", newPending, "earnings_available", newAvail, "revision", balRev, "updated_at", now)
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "mature", "ref_id", ownerID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
redis.call("HSET", opKey, "op_id", opID, "kind", "mature", "success", "1", "amount", amount, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "mature", "amount", amount, "ts", now)
return {1, "ok"}
`)

// LuaReservePayoutScript atomically moves owner earnings_available -> payout_reserved.
// KEYS: [1]=opKey, [2]=balKey, [3]=ledgerKey, [4]=headKey, [5]=outboxKey
// ARGV: [1]=amount, [2]=opID, [3]=payoutID, [4]=now, [5]=expectedPrevHash, [6]=entryHash, [7]=ledgerID, [8]=postingsJSON
var LuaReservePayoutScript = redis.NewScript(luaDecimalHelpers + `
local opKey, balKey, ledgerKey, headKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5]
local amount, opID, payoutID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8]

if not check_key_type(opKey, "hash") or not check_key_type(balKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(amount) or amount == "0" then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local curAvail, ok1 = hget_money(balKey, "earnings_available")
local curRes, ok2 = hget_money(balKey, "payout_reserved")
if not ok1 or not ok2 then return {-3, "corrupt_balance"} end

if dec_cmp(curAvail, amount) < 0 then
  return {-1, "invalid_amount"}
end
local newAvail, err1 = dec_sub(curAvail, amount)
local newRes, err2 = dec_add(curRes, amount)
if err1 or err2 then return {-1, "overflow"} end

local balRev = bump_rev(balKey)
redis.call("HSET", balKey, "earnings_available", newAvail, "payout_reserved", newRes, "revision", balRev, "updated_at", now)
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "payout_reserve", "ref_id", payoutID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
redis.call("HSET", opKey, "op_id", opID, "kind", "payout_reserve", "success", "1", "amount", amount, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "payout_reserve", "ref_id", payoutID, "amount", amount, "ts", now)
return {1, "ok"}
`)

// LuaBroadcastPayoutScript atomically clears payout_reserved, deducts cash_hot, and records network_fee_expense.
// KEYS: [1]=opKey, [2]=balKey, [3]=platKey, [4]=ledgerKey, [5]=headKey, [6]=outboxKey
// ARGV: [1]=amount, [2]=fee, [3]=opID, [4]=payoutID, [5]=now, [6]=expectedPrevHash, [7]=entryHash, [8]=ledgerID, [9]=postingsJSON
var LuaBroadcastPayoutScript = redis.NewScript(luaDecimalHelpers + `
local opKey, balKey, platKey, ledgerKey, headKey, outboxKey = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5], KEYS[6]
local amount, fee, opID, payoutID, now, expectedPrevHash, entryHash, ledgerID, postingsJSON = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7], ARGV[8], ARGV[9]

if not check_key_type(opKey, "hash") or not check_key_type(balKey, "hash") or not check_key_type(platKey, "hash") or not check_key_type(ledgerKey, "stream") or not check_key_type(headKey, "string") or not check_key_type(outboxKey, "stream") then
  return {-3, "wrong_key_type"}
end
if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
if not is_valid_money(amount) or amount == "0" or not is_valid_money(fee) then
  return {-1, "invalid_amount"}
end
local curHead = redis.call("GET", headKey)
if not curHead then curHead = GENESIS_HASH end
if curHead ~= expectedPrevHash then
  return {-2, "head_mismatch", curHead}
end

local totalOut, err0 = dec_add(amount, fee)
if err0 then return {-1, "overflow"} end

local curRes, ok1 = hget_money(balKey, "payout_reserved")
local curCash, ok2 = hget_money(platKey, "cash_hot")
local curFeeExp, ok3 = hget_money(platKey, "network_fee_expense")
if not ok1 or not ok2 or not ok3 then return {-3, "corrupt_balance"} end

if dec_cmp(curRes, amount) < 0 or dec_cmp(curCash, totalOut) < 0 then
  return {-1, "insufficient_funds"}
end
local newRes, err1 = dec_sub(curRes, amount)
local newCash, err2 = dec_sub(curCash, totalOut)
local newFeeExp, err3 = dec_add(curFeeExp, fee)
if err1 or err2 or err3 then return {-1, "overflow"} end

local balRev = bump_rev(balKey)
local platRev = bump_rev(platKey)
redis.call("HSET", balKey, "payout_reserved", newRes, "revision", balRev, "updated_at", now)
redis.call("HSET", platKey, "cash_hot", newCash, "network_fee_expense", newFeeExp, "revision", platRev, "updated_at", now)
redis.call("SET", headKey, entryHash)
redis.call("XADD", ledgerKey, "*", "ledger_id", ledgerID, "op_id", opID, "kind", "payout_broadcast", "ref_id", payoutID, "postings", postingsJSON, "prev_hash", expectedPrevHash, "entry_hash", entryHash, "ts", now)
redis.call("HSET", opKey, "op_id", opID, "kind", "payout_broadcast", "success", "1", "amount", amount, "created_at", now)
redis.call("XADD", outboxKey, "*", "op_id", opID, "kind", "payout_broadcast", "ref_id", payoutID, "amount", amount, "ts", now)
return {1, "ok"}
`)
