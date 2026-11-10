// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package genai

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestContentHelpers(t *testing.T) {
	t.Run("Text", func(t *testing.T) {
		expected := []*Content{{
			Parts: []*Part{{Text: "Hello"}},
			Role:  RoleUser,
		}}
		got := Text("Hello")
		if diff := cmp.Diff(got, expected); diff != "" {
			t.Errorf("Text mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("Content_setDefaults", func(t *testing.T) {
		expected := &Content{Parts: []*Part{{Text: "Hello"}}, Role: RoleUser}
		got := &Content{Parts: []*Part{{Text: "Hello"}}}
		got.setDefaults()
		if diff := cmp.Diff(got, expected); diff != "" {
			t.Errorf("Content.setDefaults mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("GenerateContentConfig_setDefaults", func(t *testing.T) {
		expected := &GenerateContentConfig{SystemInstruction: &Content{Parts: []*Part{{Text: "Hello"}}, Role: RoleUser}}
		got := &GenerateContentConfig{SystemInstruction: &Content{Parts: []*Part{{Text: "Hello"}}}}
		got.setDefaults()
		if diff := cmp.Diff(got, expected); diff != "" {
			t.Errorf("GenerateContentConfig.setDefaults mismatch (-want +got):\n%s", diff)
		}
	})
}
