package panel

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

var configIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// 文件配置和数据库无法共享事务；先记录操作意图，再写配置并记录结果。
func (s *Server) beginConfigChange(w http.ResponseWriter, r *http.Request, current session, action, id string) bool {
	if !configIDPattern.MatchString(id) {
		fail(w, 422, "validation_failed", "标识只能包含小写字母、数字、下划线和连字符，长度为2到64")
		return false
	}
	if err := s.store.PanelRecordAudit(r.Context(), current.Username, action+".request", id, map[string]any{}); err != nil {
		fail(w, 503, "audit_unavailable", "暂时无法记录操作，请稍后重试")
		return false
	}
	return true
}

func (s *Server) finishConfigChange(r *http.Request, current session, action, id string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	return s.store.PanelRecordAudit(ctx, current.Username, action, id, map[string]any{}) == nil
}
