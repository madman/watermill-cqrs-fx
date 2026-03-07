package cmderr

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var (
	registry = make(map[Code]Codec)
	mu       sync.RWMutex
)

type (
	// Code is an open string-based enum for error categories persisted in DB.
	Code string

	// Option allows customizing CommandError.
	Option func(*CommandError)

	// Codec handles serialization and deserialization of a specific error code.
	Codec struct {
		// Encode converts the domain error into a map of details.
		Encode func(err error) (map[string]any, error)
		// Decode reconstructs the domain error from a map of details.
		Decode func(details map[string]any) (error, error)
	}

	// CommandError is the durable, serializable representation for async command errors.
	CommandError struct {
		// Code is a stable identifier for error categories.
		Code Code `json:"code"`
		// Msg is a human-readable error message.
		Msg string `json:"msg,omitempty"`
		// Details is a map of additional context.
		Details map[string]any `json:"details,omitempty"`
		// Cause is a nested, serialized CommandError (DB-safe).
		Cause *CommandError `json:"cause,omitempty"`

		// concrete is an in-memory domain-specific error reconstructed via Decode func.
		// It is NOT serialized. This enables errors.As(err, &MyDomainError).
		concrete error
	}
)

// Register registers a coded error with a codec.
func Register(code Code, codec Codec) {
	mu.Lock()
	defer mu.Unlock()
	registry[code] = codec
}

// RegisterType is a helper that registers a type with automatic JSON codec.
func RegisterType(code Code, err error) {
	t := reflect.TypeOf(err)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	Register(code, Codec{
		Encode: func(err error) (map[string]any, error) {
			data, err := json.Marshal(err)
			if err != nil {
				return nil, err
			}
			var details map[string]any
			err = json.Unmarshal(data, &details)
			return details, err
		},
		Decode: func(details map[string]any) (error, error) {
			data, err := json.Marshal(details)
			if err != nil {
				return nil, err
			}
			val := reflect.New(t).Interface()
			if err := json.Unmarshal(data, val); err != nil {
				return nil, err
			}
			return val.(error), nil
		},
	})
}

// New creates a new CommandError from a domain error.
func New(code Code, err error) *CommandError {
	ce := &CommandError{
		Code:     code,
		Msg:      err.Error(),
		concrete: err,
	}

	mu.RLock()
	codec, ok := registry[code]
	mu.RUnlock()

	if ok && codec.Encode != nil {
		details, err := codec.Encode(err)
		if err == nil {
			ce.Details = details
		}
	}

	return ce
}

// Wrap attaches an in-memory concrete cause (not serialized) while fixing the
// durable category Code for persistence and errors.Is matching.
func Wrap(code Code, cause error, msg string, opts ...Option) *CommandError {
	e := &CommandError{Code: code, Msg: msg, concrete: cause}

	// If cause is a CommandError, we can also link it as Cause for serialization
	var ce *CommandError
	if errors.As(cause, &ce) {
		e.Cause = ce
	}

	for _, o := range opts {
		o(e)
	}
	return e
}

// Decode reconstructs a CommandError from JSON.
func Decode(data []byte) (*CommandError, error) {
	var ce CommandError
	if err := json.Unmarshal(data, &ce); err != nil {
		return nil, err
	}

	// Recursive reconstruction of concrete domain errors
	ce.reconstruct()

	return &ce, nil
}

func (e *CommandError) Error() string {
	if e.Msg == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

func (e *CommandError) Unwrap() error {
	if e.concrete != nil {
		return e.concrete
	}
	if e.Cause != nil {
		return e.Cause
	}
	return nil
}

// Is enables errors.Is(err, target) semantics across process boundaries by
// comparing category codes. Target should be a *CommandError sentinel with Code set.
func (e *CommandError) Is(target error) bool {
	if t, ok := target.(*CommandError); ok {
		return e.Code == t.Code
	}
	return false
}

// As implements errors.As support if the wrapped domain error matches.
func (e *CommandError) As(target any) bool {
	if e.concrete != nil {
		return errors.As(e.concrete, target)
	}
	return false
}

// Encode serializes the CommandError to JSON.
func (e *CommandError) Encode() ([]byte, error) {
	// Before encoding, ensure Details are fresh from concrete if possible?
	// Actually New() and Wrap() should handle it, or we do it here.
	if e.concrete != nil && e.Details == nil {
		mu.RLock()
		codec, ok := registry[e.Code]
		mu.RUnlock()
		if ok && codec.Encode != nil {
			details, err := codec.Encode(e.concrete)
			if err == nil {
				e.Details = details
			}
		}
	}
	return json.Marshal(e)
}

func (e *CommandError) reconstruct() {
	mu.RLock()
	codec, ok := registry[e.Code]
	mu.RUnlock()

	if ok && codec.Decode != nil && e.Details != nil {
		domainErr, err := codec.Decode(e.Details)
		if err == nil {
			e.concrete = domainErr
		}
	}

	if e.Cause != nil {
		e.Cause.reconstruct()
	}
}
