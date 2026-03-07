package cmderr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

type MyDomainError struct {
	Msg  string
	Code int
}

func (e *MyDomainError) Error() string {
	return e.Msg
}

type InvalidAmountError struct {
	Amount decimal.Decimal
	Places int32
}

func (e *InvalidAmountError) Error() string {
	return fmt.Sprintf("invalid amount '%s'", e.Amount.StringFixed(e.Places+1))
}

func TestRegisterAndEncodeDecode(t *testing.T) {
	// Reset registry for test
	mu.Lock()
	registry = make(map[Code]Codec)
	mu.Unlock()

	RegisterType("MY_ERROR", &MyDomainError{})

	domainErr := &MyDomainError{Msg: "something went wrong", Code: 404}
	ce := New("MY_ERROR", domainErr)

	data, err := ce.Encode()
	if err != nil {
		t.Fatalf("failed to encode error: %v", err)
	}

	decodedCe, err := Decode(data)
	if err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}

	if decodedCe.Code != "MY_ERROR" {
		t.Errorf("expected code MY_ERROR, got %s", decodedCe.Code)
	}

	// Verify errors.As
	var myErr *MyDomainError
	if !errors.As(decodedCe, &myErr) {
		t.Fatal("decoded error does not wrap MyDomainError or As() failed")
	}

	if myErr.Msg != "something went wrong" || myErr.Code != 404 {
		t.Errorf("decoded error content mismatch: %+v", myErr)
	}
}

func TestInvalidAmountError(t *testing.T) {
	// Reset registry for test
	mu.Lock()
	registry = make(map[Code]Codec)
	mu.Unlock()

	RegisterType("INVALID_AMOUNT", &InvalidAmountError{})

	domainErr := &InvalidAmountError{
		Amount: decimal.NewFromFloat(123.456),
		Places: 2,
	}

	// Test Wrap and errors.Is
	sentinel := &CommandError{Code: "INVALID_AMOUNT"}
	ce := Wrap("INVALID_AMOUNT", domainErr, "custom wrap message")

	if !errors.Is(ce, sentinel) {
		t.Error("errors.Is should return true for same Code")
	}

	data, err := ce.Encode()
	if err != nil {
		t.Fatalf("failed to encode: %v", err)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if !errors.Is(decoded, sentinel) {
		t.Error("errors.Is should work after deserialization")
	}

	var target *InvalidAmountError
	if !errors.As(decoded, &target) {
		t.Fatal("errors.As failed after deserialization")
	}

	if !target.Amount.Equal(domainErr.Amount) {
		t.Errorf("expected amount %v, got %v", domainErr.Amount, target.Amount)
	}
}

func TestRecursiveCause(t *testing.T) {
	inner := New("DB_FAIL", errors.New("connection reset"))
	outer := Wrap("COMMAND_FAIL", inner, "failed to process command")

	data, _ := outer.Encode()
	decoded, _ := Decode(data)

	if decoded.Code != "COMMAND_FAIL" {
		t.Errorf("expected COMMAND_FAIL, got %s", decoded.Code)
	}
	if decoded.Cause == nil || decoded.Cause.Code != "DB_FAIL" {
		t.Errorf("expected inner cause DB_FAIL, got %v", decoded.Cause)
	}
}
