/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"crypto/md5" //nolint:gosec // Stable short names do not need cryptographic hashing.
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	kubernetesNameMaxLength = 63
	childNameHashLength     = 8
)

func childName(parent, suffix string) string {
	if len(parent)+len(suffix) <= kubernetesNameMaxLength {
		return parent + suffix
	}

	hash := md5.Sum([]byte(parent))
	hashText := hex.EncodeToString(hash[:])[:childNameHashLength]
	prefixLength := kubernetesNameMaxLength - len(suffix) - len(hashText) - 1
	if prefixLength <= 0 {
		return hashText + suffix
	}

	prefix := strings.TrimRight(parent[:prefixLength], "-")
	if prefix == "" {
		return hashText + suffix
	}
	return fmt.Sprintf("%s-%s%s", prefix, hashText, suffix)
}
