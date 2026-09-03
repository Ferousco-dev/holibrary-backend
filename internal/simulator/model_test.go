package simulator

import (
	"math/rand"
	"testing"
)

func TestModelLoads(t *testing.T) {
	m, err := LoadModel()
	if err != nil {
		t.Fatal(err)
	}
	if m.Name == "" || len(m.Members.Archetypes) != 4 {
		t.Fatalf("model looks wrong: %+v", m.Name)
	}
}

// Borrowing must be long-tailed. Uniform sampling would look busy but would
// never reproduce the situation the system most needs to handle: everybody
// wanting the same book.
func TestZipfConcentratesDemand(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const n, draws = 100, 10000
	counts := make([]int, n)
	for i := 0; i < draws; i++ {
		counts[ZipfIndex(rng, n, 1.3)]++
	}
	topTen := 0
	for _, c := range counts[:10] {
		topTen += c
	}
	share := float64(topTen) / draws
	if share < 0.4 {
		t.Errorf("the top 10%% of titles took only %.0f%% of demand; that is too flat to form queues", share*100)
	}
	t.Logf("top 10%% of titles took %.0f%% of borrowing", share*100)
}

// Weights must be honoured, or the generated collection will not resemble a
// real one.
func TestCopyDistributionIsLongTailed(t *testing.T) {
	m, _ := LoadModel()
	rng := rand.New(rand.NewSource(2))
	counts := map[int]int{}
	for i := 0; i < 5000; i++ {
		counts[m.PickCopyCount(rng)]++
	}
	if counts[1] <= counts[8] {
		t.Errorf("single-copy titles should dominate: %v", counts)
	}
	t.Logf("copies per title: %v", counts)
}
