package main

import (
	"strconv"
	"strings"
)

func checkOutput(kind, input, got, want string) bool {
	switch kind {
	case "evacuation":
		return checkEvacuation(input, got)
	case "garden_beds":
		return checkGardenBeds(input, got, want)
	default:
		return compareOutput(kind, got, want)
	}
}

func checkEvacuation(input, got string) bool {
	values := strings.Fields(input)
	if len(values) != 6 {
		return false
	}
	v := make([]int, 6)
	for i := range v {
		var err error
		v[i], err = strconv.Atoi(values[i])
		if err != nil {
			return false
		}
	}
	n, m := v[0], v[1]
	r1, c1, r2, c2 := v[2]-1, v[3]-1, v[4]-1, v[5]-1
	rows := strings.Fields(got)
	if len(rows) != n {
		return false
	}
	grid := make([]byte, n*m)
	for r, row := range rows {
		if len(row) != m {
			return false
		}
		copy(grid[r*m:], row)
	}
	for r := 0; r < n; r++ {
		for c := 0; c < m; c++ {
			ch := grid[r*m+c]
			stair := (r == r1 && c == c1) || (r == r2 && c == c2)
			if stair != (ch == 'S') || (!stair && !strings.ContainsRune("<>^v", rune(ch))) {
				return false
			}
		}
	}

	state := make([]byte, n*m)
	distance := make([]int, n*m)
	var visit func(int) bool
	visit = func(pos int) bool {
		if state[pos] == 2 {
			return true
		}
		if state[pos] == 1 {
			return false
		}
		state[pos] = 1
		r, c := pos/m, pos%m
		if grid[pos] == 'S' {
			distance[pos] = 0
		} else {
			nr, nc := r, c
			switch grid[pos] {
			case '<':
				nc--
			case '>':
				nc++
			case '^':
				nr--
			case 'v':
				nr++
			}
			if nr < 0 || nr >= n || nc < 0 || nc >= m || !visit(nr*m+nc) {
				return false
			}
			distance[pos] = distance[nr*m+nc] + 1
		}
		state[pos] = 2
		best := absInt(r-r1) + absInt(c-c1)
		if other := absInt(r-r2) + absInt(c-c2); other < best {
			best = other
		}
		return distance[pos] == best
	}
	for pos := range grid {
		if !visit(pos) {
			return false
		}
	}
	return true
}

func checkGardenBeds(input, got, want string) bool {
	in := strings.Fields(input)
	out := strings.Fields(got)
	answer := strings.Fields(want)
	if len(in) < 5 || len(out) != 5 || len(answer) == 0 {
		return false
	}
	n, err := strconv.Atoi(in[0])
	if err != nil || len(in) != n+1 {
		return false
	}
	optimal, err := strconv.ParseInt(answer[0], 10, 64)
	if err != nil {
		return false
	}
	values := make([]int64, 5)
	for i := range values {
		values[i], err = strconv.ParseInt(out[i], 10, 64)
		if err != nil {
			return false
		}
	}
	if values[0] != optimal || values[0] < 0 {
		return false
	}
	available := make(map[int64]int, n)
	for _, token := range in[1:] {
		x, parseErr := strconv.ParseInt(token, 10, 64)
		if parseErr != nil {
			return false
		}
		available[x]++
	}
	for _, x := range values[1:] {
		if available[x] == 0 {
			return false
		}
		available[x]--
	}
	return maxInt64(absInt64(values[1]-values[2]), absInt64(values[3]-values[4])) == values[0]
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
func absInt64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
