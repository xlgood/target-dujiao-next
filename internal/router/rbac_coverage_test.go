package router

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/authz"

	"github.com/casbin/casbin/v3/util"
)

// TestAllAdminRoutesCoveredByBuiltinRoles 校验 router.go 里每条 admin 路由
// 都被 authz.BuiltinRoleSeeds() 中至少一条角色策略覆盖。
//
// 目的：避免新增 admin 接口时忘记同步 RBAC 预置角色，导致非超管角色无法通过
// 角色分配获得该权限（catalog UI 上能看到，但任何角色都拿不到）。
//
// 实现：静态扫描 router.go 提取 authorized.METHOD("/path", ...) 与
// paymentProtected.METHOD("/path", ...) 调用，与
// builtin role seeds 用 keyMatch2 比对（与运行时 Casbin 模型一致）。
func TestAllAdminRoutesCoveredByBuiltinRoles(t *testing.T) {
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatalf("extract admin routes: %v", err)
	}
	if len(routes) == 0 {
		t.Fatalf("no admin routes extracted; regex or source layout changed?")
	}

	seeds := authz.BuiltinRoleSeeds()
	if len(seeds) == 0 {
		t.Fatalf("no builtin role seeds")
	}

	type policy struct {
		object string
		action string
	}
	var policies []policy
	for _, seed := range seeds {
		for _, p := range seed.Policies {
			policies = append(policies, policy{
				object: authz.NormalizeObject(p.Object),
				action: authz.NormalizeAction(p.Action),
			})
		}
	}

	var uncovered []adminRoute
	for _, r := range routes {
		matched := false
		for _, p := range policies {
			if p.action != "*" && p.action != r.method {
				continue
			}
			if util.KeyMatch2(r.object, p.object) {
				matched = true
				break
			}
		}
		if !matched {
			uncovered = append(uncovered, r)
		}
	}

	if len(uncovered) > 0 {
		var lines []string
		for _, r := range uncovered {
			lines = append(lines, "  "+r.method+" "+r.object)
		}
		t.Fatalf("the following admin routes are not covered by any builtin role seed in authz.BuiltinRoleSeeds() — add them to the appropriate role in api/internal/authz/bootstrap.go:\n%s",
			strings.Join(lines, "\n"))
	}
}

type adminRoute struct {
	method string
	object string // 例如 "/admin/users/:id"
}

// extractAdminRoutesFromSource 从 router.go 抽取 authorized/paymentProtected admin 路由调用。
// 方法范围：GET / POST / PUT / PATCH / DELETE。HEAD/OPTIONS 不参与 RBAC。
func extractAdminRoutesFromSource() ([]adminRoute, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	routerSrc := filepath.Join(filepath.Dir(thisFile), "router.go")
	raw, err := os.ReadFile(routerSrc)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?:authorized|paymentProtected)\.(GET|POST|PUT|PATCH|DELETE)\("([^"]+)"`)
	matches := re.FindAllStringSubmatch(string(raw), -1)

	seen := make(map[string]struct{}, len(matches))
	out := make([]adminRoute, 0, len(matches))
	for _, m := range matches {
		method := m[1]
		path := m[2]
		object := authz.NormalizeObject("/admin" + path)
		key := method + " " + object
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, adminRoute{method: method, object: object})
	}
	return out, nil
}
