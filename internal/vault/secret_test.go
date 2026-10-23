package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestASecretNeverPrintsItself(t *testing.T) {
	const value = "hunter2-correct-horse"
	s := Secret(value)
	holder := struct {
		Name   string
		Fields map[string]Secret
		Ptr    *Secret
	}{"login", map[string]Secret{"password": s}, &s}

	outputs := []string{
		fmt.Sprint(s), fmt.Sprintf("%s", s), fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s),
		fmt.Sprintf("%q", s), fmt.Sprintf("%x", s), fmt.Sprintf("%d", s),
		fmt.Sprintf("%v", holder), fmt.Sprintf("%+v", holder), fmt.Sprintf("%#v", holder),
		fmt.Sprint(map[string]Secret{"p": s}), fmt.Sprint([]Secret{s}),
		fmt.Errorf("could not use %v", s).Error(),
		errors.Join(fmt.Errorf("a %w", errors.New("b")), fmt.Errorf("%s", s)).Error(),
	}
	j, err := json.Marshal(holder)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(j))
	for _, out := range outputs {
		if strings.Contains(out, value) {
			t.Errorf("the secret was printed: %s", out)
		}
	}
	if s.Reveal() != value {
		t.Fatal("Reveal must still return the value")
	}
	s.Wipe()
	if strings.Contains(string(s), "h") || s.Reveal() == value {
		t.Fatal("Wipe did not overwrite the value")
	}
}
