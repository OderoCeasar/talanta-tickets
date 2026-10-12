package config

import (
	"strings"
	"testing"
)

func TestOTPTestCodes(t *testing.T) {
	load := func(t *testing.T, value string) (API, error) {
		t.Helper()
		t.Setenv("APP_ENV", "development")
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("OTP_TEST_CODES", value)
		return LoadAPI()
	}

	t.Run("unset means none", func(t *testing.T) {
		cfg, err := load(t, "")
		if err != nil || len(cfg.OTPTestCodes) != 0 {
			t.Fatalf("got %v, %v", cfg.OTPTestCodes, err)
		}
	})

	t.Run("pairs are parsed and phones normalised", func(t *testing.T) {
		cfg, err := load(t, " +254700000011=482913 , 0700000012=771204 ")
		if err != nil {
			t.Fatalf("LoadAPI: %v", err)
		}
		if cfg.OTPTestCodes["+254700000011"] != "482913" || cfg.OTPTestCodes["+254700000012"] != "771204" || len(cfg.OTPTestCodes) != 2 {
			t.Fatalf("OTPTestCodes = %v", cfg.OTPTestCodes)
		}
	})

	refused := map[string]string{
		"no equals sign":                  "+254700000011",
		"not a phone":                     "hello=482913",
		"a country codes are not sent to": "+14155550100=482913",
		"five digits":                     "+254700000011=48291",
		"letters in the code":             "+254700000011=48a913",
		"all one digit":                   "+254700000011=000000",
		"a rising run":                    "+254700000011=123456",
		"a falling run":                   "+254700000011=654321",
		"the same phone twice":            "+254700000011=482913,0700000011=771204",
		"more than the maximum":           strings.TrimSuffix(strings.Repeat("+254700000011=482913,", MaxOTPTestCodes+1), ","),
	}
	for name, value := range refused {
		t.Run("refused: "+name, func(t *testing.T) {
			_, err := load(t, value)
			if err == nil || !strings.Contains(err.Error(), "OTP_TEST_CODES") {
				t.Fatalf("want an OTP_TEST_CODES error, got %v", err)
			}
			// A code is a credential: a config error must point at the entry, not repeat it.
			for _, secret := range []string{"482913", "771204", "48a913"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("the error repeats a code: %v", err)
				}
			}
		})
	}

	t.Run("allowed in production", func(t *testing.T) {
		productionEnv(t)
		t.Setenv("OTP_TEST_CODES", "+254700000011=482913")
		cfg, err := LoadAPI()
		if err != nil {
			t.Fatalf("LoadAPI: %v", err)
		}
		if !cfg.IsProduction() || cfg.AllowDevShortcuts() || len(cfg.OTPTestCodes) != 1 {
			t.Fatalf("production=%v devShortcuts=%v codes=%d", cfg.IsProduction(), cfg.AllowDevShortcuts(), len(cfg.OTPTestCodes))
		}
	})
}
