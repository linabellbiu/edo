package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("生成密码摘要失败: %v", err)
	}
	matched, err := ComparePassword("correct horse battery staple", encoded)
	if err != nil || !matched {
		t.Fatalf("正确密码未通过校验: matched=%v err=%v", matched, err)
	}
	matched, err = ComparePassword("wrong password", encoded)
	if err != nil {
		t.Fatalf("校验错误密码失败: %v", err)
	}
	if matched {
		t.Fatal("错误密码不应通过校验")
	}
}

func TestPasswordHashRejectsUnsafeParameters(t *testing.T) {
	encoded := "$argon2id$v=19$m=999999999,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA"
	if _, err := ComparePassword("password", encoded); err == nil {
		t.Fatal("超出安全范围的摘要参数必须被拒绝")
	}
}
