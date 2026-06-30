package flowlaunch

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func newLaunchID() string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("flowstate-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("flowstate-%d-%s", time.Now().UnixNano(), hex.EncodeToString(suffix[:]))
}
