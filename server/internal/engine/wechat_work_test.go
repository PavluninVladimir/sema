package engine

import (
	"context"
	"errors"
	"testing"
)

func TestWeChatUsesOnlyTrustedLocalModel(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	ctx := context.Background()
	const remote, local = "wechat-remote", "wechat-local"
	for _, conf := range []ModelConf{
		{Name: remote, Type: "openai", BaseURL: "https://example.com/v1", Model: "test", Trusted: true},
		{Name: local, Type: "openai", BaseURL: "http://127.0.0.1:11434/v1", Model: "test", Trusted: false},
	} {
		if err := e.AddModel(conf); err != nil {
			t.Fatal(err)
		}
	}
	cloud, home := &fakeProvider{answer: "external"}, &fakeProvider{answer: "local"}
	e.providers[remote], e.providers[local] = cloud, home
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == RoleChat {
			roles[i].Model, roles[i].Spare = remote, local
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	if _, err := e.askWeChatTrusted(ctx, "fixture"); !errors.Is(err, errNoTrusted) || cloud.calls != 0 || home.calls != 0 {
		t.Fatalf("private request went to non-eligible model: %v, cloud=%d local=%d", err, cloud.calls, home.calls)
	}
	if err := e.SetModelTrust(local, true); err != nil {
		t.Fatal(err)
	}
	e.providers[local] = home
	answer, err := e.askWeChatTrusted(ctx, "fixture")
	if err != nil || answer != "local" || cloud.calls != 0 || home.calls != 1 {
		t.Fatalf("wrong model: %v, cloud=%d local=%d", err, cloud.calls, home.calls)
	}
}
