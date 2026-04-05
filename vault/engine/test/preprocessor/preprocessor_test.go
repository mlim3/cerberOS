package preprocessor_test

import (
	"io"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

func newPP(t *testing.T) *preprocessor.Preprocessor {
	t.Helper()
	auditor := audit.New(audit.NewJSONExporter(io.Discard))
	manager := secretmanager.NewMockSecretManager(auditor)
	return preprocessor.New(manager, auditor)
}

func TestProcess(t *testing.T) {
	tests := []struct {
		name      string
		script    string
		want      string
		wantErr   bool
		setupMock func(*secretmanager.MockSecretManager)
	}{
		{
			name:   "SinglePlaceholder",
			script: "echo {{API_KEY}}",
			want:   "echo mock-api-key-12345",
		},
		{
			name:   "MultipleDifferent",
			script: "{{API_KEY}} {{DB_PASS}}",
			want:   "mock-api-key-12345 mock-db-password",
		},
		{
			name:   "DuplicatePlaceholder",
			script: "{{API_KEY}} and {{API_KEY}}",
			want:   "mock-api-key-12345 and mock-api-key-12345",
		},
		{
			name:   "NoPlaceholders",
			script: "echo hello",
			want:   "echo hello",
		},
		{
			name:   "EmptyScript",
			script: "",
			want:   "",
		},
		{
			name:   "PlaceholderOnly",
			script: "{{API_KEY}}",
			want:   "mock-api-key-12345",
		},
		{
			name:   "AdjacentPlaceholders",
			script: "{{API_KEY}}{{DB_PASS}}",
			want:   "mock-api-key-12345mock-db-password",
		},
		{
			name:   "PlaceholderInMiddle",
			script: "a{{API_KEY}}b",
			want:   "amock-api-key-12345b",
		},
		{
			name:   "InvalidSyntax_SingleBrace",
			script: "{API_KEY}",
			want:   "{API_KEY}",
		},
		{
			name:   "InvalidSyntax_Spaces",
			script: "{{ API_KEY }}",
			want:   "{{ API_KEY }}",
		},
		{
			name:   "InvalidSyntax_StartsWithDigit",
			script: "{{123KEY}}",
			want:   "{{123KEY}}",
		},
		{
			name:   "NestedBraces",
			script: "{{{API_KEY}}}",
			want:   "{mock-api-key-12345}",
		},
		{
			name:   "MixedValidInvalid",
			script: "{{API_KEY}} {nope} {{DB_PASS}}",
			want:   "mock-api-key-12345 {nope} mock-db-password",
		},
		{
			name:    "UnknownKey_ReturnsError",
			script:  "{{DOES_NOT_EXIST}}",
			wantErr: true,
		},
		{
			name:    "MixedKnownUnknown_AtomicError",
			script:  "{{API_KEY}} {{NOPE}}",
			wantErr: true,
		},
		{
			name:   "UnderscoreOnlyKey",
			script: "{{_}}",
			setupMock: func(m *secretmanager.MockSecretManager) {
				m.PutSecret(nil, "_", "underscore-val")
			},
			want: "underscore-val",
		},
		{
			name:   "AllFourSeededKeys",
			script: "{{API_KEY}},{{DB_PASS}},{{SECRET_KEY}},{{TEST_SECRET}}",
			want:   "mock-api-key-12345,mock-db-password,mock-secret-key,hello-from-secretstore",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auditor := audit.New(audit.NewJSONExporter(io.Discard))
			manager := secretmanager.NewMockSecretManager(auditor)
			if tt.setupMock != nil {
				tt.setupMock(manager)
			}
			pp := preprocessor.New(manager, auditor)

			result, err := pp.Process("test-agent", []byte(tt.script))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(result.Script) != tt.want {
				t.Fatalf("script = %q, want %q", string(result.Script), tt.want)
			}
		})
	}
}

func TestProcess_Result(t *testing.T) {
	t.Run("InjectedSecrets_ContainsValues", func(t *testing.T) {
		pp := newPP(t)
		result, err := pp.Process("a", []byte("{{API_KEY}}"))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.InjectedSecrets) == 0 {
			t.Fatal("expected InjectedSecrets to be non-empty")
		}
		found := false
		for _, v := range result.InjectedSecrets {
			if v == "mock-api-key-12345" {
				found = true
			}
		}
		if !found {
			t.Fatalf("mock-api-key-12345 not in InjectedSecrets: %v", result.InjectedSecrets)
		}
	})

	t.Run("InjectedSecrets_Empty_WhenNoPlaceholders", func(t *testing.T) {
		pp := newPP(t)
		result, err := pp.Process("a", []byte("echo hello"))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.InjectedSecrets) != 0 {
			t.Fatalf("expected empty InjectedSecrets, got %v", result.InjectedSecrets)
		}
	})

	t.Run("Script_IsByteEqual", func(t *testing.T) {
		pp := newPP(t)
		result, err := pp.Process("a", []byte("echo {{DB_PASS}}"))
		if err != nil {
			t.Fatal(err)
		}
		want := []byte("echo mock-db-password")
		if string(result.Script) != string(want) {
			t.Fatalf("script bytes = %q, want %q", result.Script, want)
		}
	})
}
