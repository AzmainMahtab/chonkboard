package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"exactly the minimum", strings.Repeat("a", MinPasswordLength), false},
		{"comfortably long", "correct horse battery staple", false},
		{"unicode counts runes, not bytes", "påsswördé🔐", true},
		{"unicode long enough", "påsswördéé-ünïcødé", false},
		{"one below the minimum", strings.Repeat("a", MinPasswordLength-1), true},
		{"empty", "", true},
		{"at the ceiling", strings.Repeat("a", MaxPasswordLength), false},
		{"past the ceiling", strings.Repeat("a", MaxPasswordLength+1), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.in)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, apperrors.New(apperrors.CodeValidation, ""))
			require.Len(t, apperrors.From(err).Fields, 1)
			assert.Equal(t, "password", apperrors.From(err).Fields[0].Field)
		})
	}
}

func TestValidatePasswordHasNoCompositionRules(t *testing.T) {
	// Deliberate: composition rules push people towards predictable
	// substitutions and away from length, which is what actually matters.
	assert.NoError(t, ValidatePassword("aaaaaaaaaaaa"))
	assert.NoError(t, ValidatePassword("all lower case words"))
}

func TestGeneratePassword(t *testing.T) {
	seen := make(map[string]struct{}, 500)

	for range 500 {
		pw, err := GeneratePassword()
		require.NoError(t, err)

		assert.Len(t, pw, GeneratedPasswordLength)
		assert.NoError(t, ValidatePassword(pw), "a generated password must pass our own policy")

		_, duplicate := seen[pw]
		require.False(t, duplicate, "generated the same password twice: %s", pw)
		seen[pw] = struct{}{}

		for _, r := range pw {
			assert.Contains(t, generatedAlphabet, string(r),
				"unexpected character %q in %q", r, pw)
		}
	}
}

func TestGeneratedPasswordsAvoidConfusableCharacters(t *testing.T) {
	// This credential is read off a screen and dictated to somebody, so a 0/O or
	// 1/l mix-up is a real support cost.
	for _, confusable := range []string{"0", "O", "1", "l", "I"} {
		assert.NotContains(t, generatedAlphabet, confusable,
			"%q is easy to misread and must not be in the alphabet", confusable)
	}
}
