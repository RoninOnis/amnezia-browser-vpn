package main

import (
	"reflect"
	"testing"
)

func pairs(attempts []PortMtuAttempt) [][2]int {
	out := make([][2]int, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, [2]int{a.Port, a.MTU})
	}
	return out
}

// Порядок попыток: свой порт с MTU профиля, понижение MTU, затем то же на 443.
func TestAttemptPlanNormalNetwork(t *testing.T) {
	got := pairs(AttemptPlan(37689, 1376, true))
	want := [][2]int{{37689, 1376}, {37689, 1280}, {443, 1376}, {443, 1280}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPlan = %v, want %v", got, want)
	}
}

// Профиль уже на 443 — дублировать не надо.
func TestAttemptPlanNoDuplicateOn443(t *testing.T) {
	got := pairs(AttemptPlan(443, 1376, true))
	want := [][2]int{{443, 1376}, {443, 1280}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPlan = %v, want %v", got, want)
	}
}

// Запасной порт можно выключить — тогда только свой порт.
func TestAttemptPlanFallbackOff(t *testing.T) {
	got := pairs(AttemptPlan(36853, 1420, false))
	want := [][2]int{{36853, 1420}, {36853, 1280}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPlan = %v, want %v", got, want)
	}
}

// MTU уже маленький — понижать некуда, порт всё равно проверяем.
func TestAttemptPlanSmallMtu(t *testing.T) {
	got := pairs(AttemptPlan(36853, 1200, true))
	want := [][2]int{{36853, 1200}, {443, 1200}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPlan = %v, want %v", got, want)
	}
}

// Пустые значения подставляются разумными: порт 443, MTU 1420.
func TestAttemptPlanDefaults(t *testing.T) {
	got := pairs(AttemptPlan(0, 0, true))
	want := [][2]int{{443, 1420}, {443, 1280}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPlan = %v, want %v", got, want)
	}
}

// Первая попытка всегда ровно та, что в профиле: на нормальной сети ничего не меняется.
func TestAttemptPlanStartsFromProfile(t *testing.T) {
	first := AttemptPlan(37689, 1376, true)[0]
	if first.Port != 37689 || first.MTU != 1376 {
		t.Fatalf("первая попытка %+v, ожидалась 37689/1376", first)
	}
}

// Попыток не больше четырёх: иначе перебор не влезет в таймаут расширения
// при handshakeWait = 4 s.
func TestAttemptPlanBounded(t *testing.T) {
	if n := len(AttemptPlan(37689, 1420, true)); n > 4 {
		t.Fatalf("попыток %d, ожидалось не больше 4", n)
	}
}

// Через релей MTU урезается: там ещё один хоп.
func TestMaskToken(t *testing.T) {
	if maskToken("") != "(без токена)" {
		t.Fatalf("пустой токен: %q", maskToken(""))
	}
	if maskToken("abc") != "***" {
		t.Fatalf("короткий токен: %q", maskToken("abc"))
	}
	long := "0123456789abcdef"
	got := maskToken(long)
	if got == long {
		t.Fatal("длинный токен не должен попадать в лог целиком")
	}
	if got != "0123***ef" {
		t.Fatalf("маска: %q", got)
	}
}
