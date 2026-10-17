// Package validation turns struct-tag validation into messages that are safe to
// show a user.
//
// go-playground/validator produces error values keyed by Go struct field names.
// This wrapper re-keys them by their JSON name and words them for a UI, so every
// module reports field problems in the same shape as the shared error envelope.
package validation

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Validator validates request DTOs.
type Validator struct {
	v *validator.Validate
}

// New returns a Validator that reports fields by their JSON name.
func New() *Validator {
	v := validator.New()

	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
		if name == "" || name == "-" {
			return field.Name
		}
		return name
	})

	return &Validator{v: v}
}

// FieldErrors returns nil when the value is valid, otherwise one message per
// offending field keyed by JSON name.
//
// A DTO that is not a struct at all is a programming error, reported against a
// placeholder key rather than panicking.
func (val *Validator) FieldErrors(s any) map[string]string {
	if s == nil {
		return nil
	}

	err := val.v.Struct(s)
	if err == nil {
		return nil
	}

	var validationErrs validator.ValidationErrors
	if !errors.As(err, &validationErrs) {
		return map[string]string{"_": "The request could not be validated."}
	}

	out := make(map[string]string, len(validationErrs))
	for _, fieldErr := range validationErrs {
		out[fieldErr.Field()] = message(fieldErr)
	}
	return out
}

// Valid reports whether the value passes validation.
func (val *Validator) Valid(s any) bool {
	return len(val.FieldErrors(s)) == 0
}

// message words a single validation failure.
func message(fieldErr validator.FieldError) string {
	switch fieldErr.Tag() {
	case "required":
		return "This field is required."
	case "email":
		return "Enter a valid email address."
	case "url":
		return "Enter a valid link."
	case "min":
		return fmt.Sprintf("Must be at least %s characters.", fieldErr.Param())
	case "max":
		return fmt.Sprintf("Must be at most %s characters.", fieldErr.Param())
	case "oneof":
		return fmt.Sprintf("Must be one of: %s.", strings.ReplaceAll(fieldErr.Param(), " ", ", "))
	case "gte":
		return fmt.Sprintf("Must be %s or more.", fieldErr.Param())
	case "lte":
		return fmt.Sprintf("Must be %s or less.", fieldErr.Param())
	case "numeric":
		return "Must be a number."
	default:
		return "This value is not valid."
	}
}
