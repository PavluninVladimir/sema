package store

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Потребители прыжками по индексу — ровно те же, что сканом: скан здесь
// оракул, а не мёртвый код.
func TestConsumersByJumpsMatchScan(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 40; i++ {
		if err := s.AddUsage(&UsageRow{Consumer: fmt.Sprintf("module:%02d", i%7), Model: "м",
			At: time.Now(), TokensIn: 1}); err != nil {
			t.Fatal(err)
		}
	}
	byJumps, byScan := s.UsageConsumers(), s.usageConsumersScan()
	if len(byJumps) != 7 || !reflect.DeepEqual(byJumps, byScan) {
		t.Fatalf("прыжками %v, сканом %v", byJumps, byScan)
	}
}
