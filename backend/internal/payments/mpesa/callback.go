package mpesa

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

// ErrBadCallback is returned by ParseCallback for a body that is not Daraja's STK envelope.
var ErrBadCallback = errors.New("mpesa: not an STK callback")

// ParseCallback reads Daraja's STK push callback, exactly as Safaricom sends it:
//
//	{"Body":{"stkCallback":{"MerchantRequestID":…,"CheckoutRequestID":…,"ResultCode":0,
//	  "ResultDesc":…,"CallbackMetadata":{"Item":[{"Name":"Amount","Value":1000.00},…]}}}}
//
// Unknown fields are accepted. The amount is read from the JSON number's own digits into
// cents with no float in between; an amount that is missing, negative, or has more than two
// decimals is left nil, which the payment flow treats as "cannot be verified".
func ParseCallback(raw []byte) (payments.Callback, error) {
	var env struct {
		Body struct {
			Callback *struct {
				MerchantRequestID string      `json:"MerchantRequestID"`
				CheckoutRequestID string      `json:"CheckoutRequestID"`
				ResultCode        json.Number `json:"ResultCode"`
				ResultDesc        string      `json:"ResultDesc"`
				Metadata          struct {
					Item []struct {
						Name  string          `json:"Name"`
						Value json.RawMessage `json:"Value"`
					} `json:"Item"`
				} `json:"CallbackMetadata"`
			} `json:"stkCallback"`
		} `json:"Body"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil || env.Body.Callback == nil {
		return payments.Callback{}, ErrBadCallback
	}
	c := env.Body.Callback
	code, err := c.ResultCode.Int64()
	if err != nil || c.CheckoutRequestID == "" {
		return payments.Callback{}, ErrBadCallback
	}
	cb := payments.Callback{
		CheckoutRequestID: c.CheckoutRequestID, MerchantRequestID: c.MerchantRequestID,
		ResultCode: int(code), ResultDesc: c.ResultDesc, Raw: raw,
	}
	for _, item := range c.Metadata.Item {
		value := strings.TrimSpace(string(item.Value))
		switch item.Name {
		case "Amount":
			if cents, ok := shillingsToCents(strings.Trim(value, `"`)); ok {
				cb.AmountCents = &cents
			}
		case "MpesaReceiptNumber":
			var receipt string
			if json.Unmarshal(item.Value, &receipt) == nil {
				cb.Receipt = strings.TrimSpace(receipt)
			}
		}
	}
	return cb, nil
}

// shillingsToCents reads a decimal number of shillings ("1000", "1000.0", "1000.50") into
// cents. It works on the digits: no float touches money.
func shillingsToCents(s string) (int64, bool) {
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		// "1000.000" is still a thousand shillings; "1000.005" is not an amount of money
		// M-Pesa can have moved.
		if strings.Trim(frac[2:], "0") != "" {
			return 0, false
		}
		frac = frac[:2]
	}
	if whole == "" || len(whole) > 12 {
		return 0, false
	}
	var cents int64
	for _, c := range whole {
		if c < '0' || c > '9' {
			return 0, false
		}
		cents = cents*10 + int64(c-'0')
	}
	cents *= 100
	for len(frac) < 2 {
		frac += "0"
	}
	for i, c := range frac {
		if c < '0' || c > '9' {
			return 0, false
		}
		if i == 0 {
			cents += int64(c-'0') * 10
		} else {
			cents += int64(c - '0')
		}
	}
	return cents, true
}
