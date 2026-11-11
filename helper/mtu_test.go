package main

import (
	"reflect"
	"testing"
)

func TestMTULadder(t *testing.T) {
	cases := []struct {
		base int
		want []int
	}{
		{1376, []int{1376, 1280, 1240, 1200}},
		{1420, []int{1420, 1280, 1240, 1200}},
		{1280, []int{1280, 1240, 1200}},
		{1240, []int{1240, 1200}},
		{1200, []int{1200}},
		{1000, []int{1000}},
		{0, []int{1420, 1280, 1240, 1200}}, // MTU не задан — начинаем с дефолта профиля
	}
	for _, c := range cases {
		if got := MTULadder(c.base); !reflect.DeepEqual(got, c.want) {
			t.Errorf("MTULadder(%d) = %v, want %v", c.base, got, c.want)
		}
	}
}

// Первое значение всегда равно MTU профиля: если сеть нормальная, подбор ничего не меняет.
func TestMTULadderStartsFromProfile(t *testing.T) {
	for _, base := range []int{1420, 1376, 1300} {
		if got := MTULadder(base)[0]; got != base {
			t.Fatalf("первый шаг %d, ожидался MTU профиля %d", got, base)
		}
	}
}
