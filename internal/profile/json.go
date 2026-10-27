// Copyright 2026 Numspot.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package profile resolves immutable configuration and explicit override policy.
// It performs no API calls, resource writes or credential lookups.
package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"
)

var (
	// ErrInvalidJSON identifies input that cannot be interpreted as a JSON object.
	ErrInvalidJSON = errors.New("invalid profile JSON object")
	// ErrInvalidProfile identifies a missing profile configuration.
	ErrInvalidProfile = errors.New("profile configuration is required")
)

func decodeObject(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, ErrInvalidJSON
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidJSON
	}
	return object, nil
}

// canonicalNumber keeps a coefficient and exponent rather than expanding a
// potentially large exponent. Storage and work remain bounded by input size.
func canonicalNumber(number json.Number) (json.Number, error) {
	text := strings.ToLower(string(number))
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	mantissa, exponentText, hasExponent := strings.Cut(text, "e")
	exponent := new(big.Int)
	if hasExponent {
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return "", ErrInvalidJSON
		}
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	exponent.Sub(exponent, big.NewInt(int64(len(fraction))))
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return json.Number("0"), nil
	}
	coefficient := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(coefficient))))
	if negative {
		coefficient = "-" + coefficient
	}
	if exponent.Sign() != 0 {
		coefficient += "e" + exponent.String()
	}
	return json.Number(coefficient), nil
}
