package scim

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

type Handler struct {
	Store *database.Store
	Cfg   *config.Config
	Auth  *auth.Authenticator
}

func NewHandler(store *database.Store, cfg *config.Config, authenticator *auth.Authenticator) *Handler {
	return &Handler{Store: store, Cfg: cfg, Auth: authenticator}
}

func (h *Handler) BearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.Cfg.SCIMBearerToken)) != 1 {
			scimError(w, 401, "Unauthorized", "Invalid or missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) ServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, 200, map[string]interface{}{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          map[string]interface{}{"supported": true},
		"bulk":           map[string]interface{}{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]interface{}{"supported": true, "maxResults": 100},
		"changePassword": map[string]interface{}{"supported": false},
		"sort":           map[string]interface{}{"supported": false},
		"etag":           map[string]interface{}{"supported": false},
		"authenticationSchemes": []map[string]interface{}{
			{
				"type":        "oauthbearertoken",
				"name":        "HTTP Bearer",
				"description": "Authentication via bearer token",
				"primary":     true,
			},
		},
	})
}

func (h *Handler) Schemas(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, 200, []map[string]interface{}{
		userSchema(),
		groupSchema(),
	})
}

func (h *Handler) SchemaUser(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, 200, userSchema())
}

func (h *Handler) SchemaGroup(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, 200, groupSchema())
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	startIndex, count := parseListParams(r)
	filter := r.URL.Query().Get("filter")

	users, err := h.Store.ListUsers()
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	if filter != "" {
		users = filterUsers(users, filter)
	}

	total := len(users)
	if startIndex > total {
		startIndex = total
	}
	end := startIndex + count
	if end > total {
		end = total
	}

	page := users[startIndex:end]

	var resources []map[string]interface{}
	for _, u := range page {
		resources = append(resources, h.userToSCIM(u))
	}

	scimJSON(w, 200, map[string]interface{}{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": total,
		"startIndex":   startIndex + 1,
		"itemsPerPage": len(page),
		"Resources":    resources,
	})
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid user ID")
		return
	}

	user, err := h.Store.GetUserByID(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if user == nil {
		scimError(w, 404, "NotFound", "User not found")
		return
	}

	scimJSON(w, 200, h.userToSCIM(*user))
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req scimUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, 400, "invalidSyntax", "Invalid JSON")
		return
	}

	userName := req.UserName
	if userName == "" {
		scimError(w, 400, "invalidValue", "userName (email) is required")
		return
	}

	existing, err := h.Store.GetUserByEmail(userName)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if existing != nil {
		scimError(w, 409, "uniqueness", "User already exists")
		return
	}

	externalID := req.ExternalID
	displayName := req.DisplayName
	if displayName == "" && len(req.Name.GivenName) > 0 {
		displayName = req.Name.GivenName
		if req.Name.FamilyName != "" {
			displayName += " " + req.Name.FamilyName
		}
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}

	role := h.resolveRoleFromGroups(req.Groups)

	user, err := h.Store.CreateUser(userName, role)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	if externalID != "" || displayName != "" {
		if err := h.Store.UpdateUserSCIM(user.ID, externalID, displayName, active); err != nil {
			scimError(w, 500, "InternalServerError", err.Error())
			return
		}
	}

	user.ExternalID = externalID
	user.DisplayName = displayName
	user.Active = active
	user.Role = role

	if len(req.Groups) > 0 {
		h.syncUserGroups(user.ID, req.Groups)
	}

	scimJSON(w, 201, h.userToSCIM(*user))
}

func (h *Handler) ReplaceUser(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid user ID")
		return
	}

	user, err := h.Store.GetUserByID(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if user == nil {
		scimError(w, 404, "NotFound", "User not found")
		return
	}

	var req scimUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, 400, "invalidSyntax", "Invalid JSON")
		return
	}

	displayName := req.DisplayName
	if displayName == "" && len(req.Name.GivenName) > 0 {
		displayName = req.Name.GivenName
		if req.Name.FamilyName != "" {
			displayName += " " + req.Name.FamilyName
		}
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}

	if err := h.Store.UpdateUserSCIM(user.ID, req.ExternalID, displayName, active); err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	role := h.resolveRoleFromGroups(req.Groups)
	if role != user.Role {
		if err := h.Store.UpdateUserRole(user.ID, role); err != nil {
			scimError(w, 500, "InternalServerError", err.Error())
			return
		}
	}

	user.ExternalID = req.ExternalID
	user.DisplayName = displayName
	user.Active = active
	user.Role = role

	if len(req.Groups) > 0 {
		h.syncUserGroups(user.ID, req.Groups)
	}

	h.triggerSessionInvalidation(user)

	scimJSON(w, 200, h.userToSCIM(*user))
}

func (h *Handler) PatchUser(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid user ID")
		return
	}

	user, err := h.Store.GetUserByID(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if user == nil {
		scimError(w, 404, "NotFound", "User not found")
		return
	}

	var req scimPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, 400, "invalidSyntax", "Invalid JSON")
		return
	}

	needsSessionInvalidate := false

	for _, op := range req.Operations {
		switch op.Op {
		case "replace":
			switch op.Path {
			case "active":
				if active, ok := op.Value.(bool); ok {
					h.Store.SetUserActive(user.ID, active)
					user.Active = active
					needsSessionInvalidate = true
				}
			case "displayName":
				if dn, ok := op.Value.(string); ok {
					h.Store.UpdateUserSCIM(user.ID, user.ExternalID, dn, user.Active)
					user.DisplayName = dn
				}
			case "externalId":
				if eid, ok := op.Value.(string); ok {
					h.Store.UpdateUserSCIM(user.ID, eid, user.DisplayName, user.Active)
					user.ExternalID = eid
				}
			default:
				if op.Path == "" && op.Value != nil {
					if m, ok := op.Value.(map[string]interface{}); ok {
						if active, ok := m["active"].(bool); ok {
							h.Store.SetUserActive(user.ID, active)
							user.Active = active
							needsSessionInvalidate = true
						}
						if dn, ok := m["displayName"].(string); ok {
							h.Store.UpdateUserSCIM(user.ID, user.ExternalID, dn, user.Active)
							user.DisplayName = dn
						}
					}
				}
			}
		case "add":
			if op.Path == "groups" {
				if groupRefs, ok := op.Value.([]interface{}); ok {
					for _, ref := range groupRefs {
						if gmap, ok := ref.(map[string]interface{}); ok {
							if val, ok := gmap["value"].(string); ok {
								gid, _ := strconv.Atoi(val)
								h.Store.AddSCIMGroupMember(gid, user.ID)
								needsSessionInvalidate = true
							}
						}
					}
				}
			}
		case "remove":
			if op.Path == "active" {
				h.Store.SetUserActive(user.ID, false)
				user.Active = false
				needsSessionInvalidate = true
			} else if strings.HasPrefix(op.Path, "groups[value eq") {
				parts := strings.Split(op.Path, "\"")
				if len(parts) >= 2 {
					gid, _ := strconv.Atoi(parts[1])
					h.Store.RemoveSCIMGroupMember(gid, user.ID)
					needsSessionInvalidate = true
				}
			}
		}
	}

	if needsSessionInvalidate {
		h.recalcUserRole(user)
		h.triggerSessionInvalidation(user)
	}

	scimJSON(w, 200, h.userToSCIM(*user))
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid user ID")
		return
	}

	user, err := h.Store.GetUserByID(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if user == nil {
		scimError(w, 404, "NotFound", "User not found")
		return
	}

	if h.Auth != nil {
		h.Store.DeleteSessionsBySub(user.ExternalID)
	}

	h.Store.SetUserActive(user.ID, false)

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	startIndex, count := parseListParams(r)

	groups, err := h.Store.ListSCIMGroups()
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	total := len(groups)
	if startIndex > total {
		startIndex = total
	}
	end := startIndex + count
	if end > total {
		end = total
	}

	page := groups[startIndex:end]

	var resources []map[string]interface{}
	for _, g := range page {
		resources = append(resources, h.groupToSCIM(g))
	}

	scimJSON(w, 200, map[string]interface{}{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": total,
		"startIndex":   startIndex + 1,
		"itemsPerPage": len(page),
		"Resources":    resources,
	})
}

func (h *Handler) GetGroup(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid group ID")
		return
	}

	group, err := h.Store.GetSCIMGroup(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if group == nil {
		scimError(w, 404, "NotFound", "Group not found")
		return
	}

	scimJSON(w, 200, h.groupToSCIM(*group))
}

func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var req scimGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, 400, "invalidSyntax", "Invalid JSON")
		return
	}

	if req.DisplayName == "" {
		scimError(w, 400, "invalidValue", "displayName is required")
		return
	}

	existing, _ := h.Store.GetSCIMGroupByName(req.DisplayName)
	if existing != nil {
		scimError(w, 409, "uniqueness", "Group already exists")
		return
	}

	group := &database.SCIMGroup{
		ExternalID:  req.ExternalID,
		DisplayName: req.DisplayName,
	}

	if err := h.Store.CreateSCIMGroup(group); err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	if len(req.Members) > 0 {
		var userIDs []int
		for _, m := range req.Members {
			uid, _ := strconv.Atoi(m.Value)
			if uid > 0 {
				userIDs = append(userIDs, uid)
			}
		}
		h.Store.SetSCIMGroupMembers(group.ID, userIDs)
	}

	scimJSON(w, 201, h.groupToSCIM(*group))
}

func (h *Handler) ReplaceGroup(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid group ID")
		return
	}

	group, err := h.Store.GetSCIMGroup(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if group == nil {
		scimError(w, 404, "NotFound", "Group not found")
		return
	}

	var req scimGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, 400, "invalidSyntax", "Invalid JSON")
		return
	}

	if req.DisplayName != "" {
		h.Store.UpdateSCIMGroup(id, req.ExternalID, req.DisplayName)
		group.DisplayName = req.DisplayName
		group.ExternalID = req.ExternalID
	}

	var userIDs []int
	for _, m := range req.Members {
		uid, _ := strconv.Atoi(m.Value)
		if uid > 0 {
			userIDs = append(userIDs, uid)
		}
	}
	h.Store.SetSCIMGroupMembers(id, userIDs)

	h.invalidateGroupMemberSessions(id)

	scimJSON(w, 200, h.groupToSCIM(*group))
}

func (h *Handler) PatchGroup(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid group ID")
		return
	}

	group, err := h.Store.GetSCIMGroup(id)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if group == nil {
		scimError(w, 404, "NotFound", "Group not found")
		return
	}

	var req scimPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, 400, "invalidSyntax", "Invalid JSON")
		return
	}

	for _, op := range req.Operations {
		switch op.Op {
		case "add":
			if op.Path == "members" {
				if refs, ok := op.Value.([]interface{}); ok {
					for _, ref := range refs {
						if m, ok := ref.(map[string]interface{}); ok {
							if val, ok := m["value"].(string); ok {
								uid, _ := strconv.Atoi(val)
								if uid > 0 {
									h.Store.AddSCIMGroupMember(id, uid)
								}
							}
						}
					}
				}
			}
		case "remove":
			if strings.HasPrefix(op.Path, "members[value eq") {
				parts := strings.Split(op.Path, "\"")
				if len(parts) >= 2 {
					uid, _ := strconv.Atoi(parts[1])
					if uid > 0 {
						h.Store.RemoveSCIMGroupMember(id, uid)
					}
				}
			} else if op.Path == "members" {
				h.Store.SetSCIMGroupMembers(id, nil)
			}
		case "replace":
			if op.Path == "displayName" {
				if dn, ok := op.Value.(string); ok {
					h.Store.UpdateSCIMGroup(id, group.ExternalID, dn)
					group.DisplayName = dn
				}
			}
		}
	}

	h.invalidateGroupMemberSessions(id)

	scimJSON(w, 200, h.groupToSCIM(*group))
}

func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		scimError(w, 400, "invalidValue", "Invalid group ID")
		return
	}

	members, _ := h.Store.GetSCIMGroupMembers(id)

	if err := h.Store.DeleteSCIMGroup(id); err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	for _, m := range members {
		h.recalcUserRole(&m)
		h.triggerSessionInvalidation(&m)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) userToSCIM(u database.User) map[string]interface{} {
	groups, _ := h.Store.GetSCIMGroupsForUser(u.ID)
	var groupRefs []map[string]interface{}
	for _, g := range groups {
		groupRefs = append(groupRefs, map[string]interface{}{
			"value":   fmt.Sprintf("%d", g.ID),
			"display": g.DisplayName,
			"$ref":    fmt.Sprintf("/scim/v2/Groups/%d", g.ID),
		})
	}

	result := map[string]interface{}{
		"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"id":       fmt.Sprintf("%d", u.ID),
		"userName": u.Email,
		"active":   u.Active,
		"meta": map[string]interface{}{
			"resourceType": "User",
			"location":     fmt.Sprintf("/scim/v2/Users/%d", u.ID),
		},
	}

	if u.ExternalID != "" {
		result["externalId"] = u.ExternalID
	}
	if u.DisplayName != "" {
		result["displayName"] = u.DisplayName
		result["name"] = map[string]interface{}{
			"formatted": u.DisplayName,
		}
	}
	if len(groupRefs) > 0 {
		result["groups"] = groupRefs
	}

	return result
}

func (h *Handler) groupToSCIM(g database.SCIMGroup) map[string]interface{} {
	members, _ := h.Store.GetSCIMGroupMembers(g.ID)
	var memberRefs []map[string]interface{}
	for _, m := range members {
		memberRefs = append(memberRefs, map[string]interface{}{
			"value":   fmt.Sprintf("%d", m.ID),
			"display": m.DisplayName,
			"$ref":    fmt.Sprintf("/scim/v2/Users/%d", m.ID),
		})
	}

	result := map[string]interface{}{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
		"id":          fmt.Sprintf("%d", g.ID),
		"displayName": g.DisplayName,
		"meta": map[string]interface{}{
			"resourceType": "Group",
			"location":     fmt.Sprintf("/scim/v2/Groups/%d", g.ID),
		},
	}

	if g.ExternalID != "" {
		result["externalId"] = g.ExternalID
	}
	if len(memberRefs) > 0 {
		result["members"] = memberRefs
	}

	return result
}

func (h *Handler) resolveRoleFromGroups(groupRefs []scimGroupRef) string {
	adminGroup := h.Cfg.OIDCAdminGroup
	userGroup := h.Cfg.OIDCUserGroup

	for _, ref := range groupRefs {
		gid, _ := strconv.Atoi(ref.Value)
		if gid <= 0 {
			continue
		}
		group, _ := h.Store.GetSCIMGroup(gid)
		if group == nil {
			continue
		}
		if adminGroup != "" && group.DisplayName == adminGroup {
			return "admin"
		}
		if userGroup != "" && group.DisplayName == userGroup {
			return "user"
		}
	}

	return "user"
}

func (h *Handler) recalcUserRole(user *database.User) {
	groups, _ := h.Store.GetSCIMGroupsForUser(user.ID)
	var groupNames []string
	for _, g := range groups {
		groupNames = append(groupNames, g.DisplayName)
	}

	role := h.roleFromGroupNames(groupNames)
	if role != user.Role {
		h.Store.UpdateUserRole(user.ID, role)
		user.Role = role
	}
}

func (h *Handler) roleFromGroupNames(names []string) string {
	adminGroup := h.Cfg.OIDCAdminGroup
	userGroup := h.Cfg.OIDCUserGroup

	for _, n := range names {
		if adminGroup != "" && n == adminGroup {
			return "admin"
		}
	}
	for _, n := range names {
		if userGroup != "" && n == userGroup {
			return "user"
		}
	}
	return "user"
}

func (h *Handler) syncUserGroups(userID int, groupRefs []scimGroupRef) {
	for _, ref := range groupRefs {
		gid, _ := strconv.Atoi(ref.Value)
		if gid > 0 {
			h.Store.AddSCIMGroupMember(gid, userID)
		}
	}
}

func (h *Handler) triggerSessionInvalidation(user *database.User) {
	if h.Auth == nil {
		return
	}

	if user.ExternalID != "" {
		if err := h.Store.DeleteSessionsBySub(user.ExternalID); err != nil {
			log.Printf("SCIM: failed to invalidate sessions for user %s: %v", user.Email, err)
		}
	}
}

func (h *Handler) invalidateGroupMemberSessions(groupID int) {
	members, _ := h.Store.GetSCIMGroupMembers(groupID)
	for _, m := range members {
		h.recalcUserRole(&m)
		h.triggerSessionInvalidation(&m)
	}
}

func filterUsers(users []database.User, filter string) []database.User {
	if strings.Contains(filter, "userName eq") {
		parts := strings.Split(filter, "\"")
		if len(parts) >= 2 {
			email := parts[1]
			var result []database.User
			for _, u := range users {
				if u.Email == email {
					result = append(result, u)
				}
			}
			return result
		}
	}
	if strings.Contains(filter, "externalId eq") {
		parts := strings.Split(filter, "\"")
		if len(parts) >= 2 {
			eid := parts[1]
			var result []database.User
			for _, u := range users {
				if u.ExternalID == eid {
					result = append(result, u)
				}
			}
			return result
		}
	}
	if strings.Contains(filter, "displayName eq") {
		parts := strings.Split(filter, "\"")
		if len(parts) >= 2 {
			dn := parts[1]
			var result []database.User
			for _, u := range users {
				if u.DisplayName == dn {
					result = append(result, u)
				}
			}
			return result
		}
	}
	return users
}

func parseListParams(r *http.Request) (startIndex, count int) {
	startIndex = 0
	count = 100
	if v := r.URL.Query().Get("startIndex"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			startIndex = n - 1
		}
	}
	if v := r.URL.Query().Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			count = n
		}
	}
	return
}

func scimJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/scim+json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func scimError(w http.ResponseWriter, status int, scimType, detail string) {
	w.Header().Set("Content-Type", "application/scim+json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"schemas":  []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"status":   fmt.Sprintf("%d", status),
		"detail":   detail,
		"scimType": scimType,
	})
}

type scimUserRequest struct {
	Schemas     []string       `json:"schemas"`
	ExternalID  string         `json:"externalId"`
	UserName    string         `json:"userName"`
	DisplayName string         `json:"displayName"`
	Name        scimName       `json:"name"`
	Active      *bool          `json:"active"`
	Groups      []scimGroupRef `json:"groups"`
}

type scimName struct {
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
	Formatted  string `json:"formatted"`
}

type scimGroupRef struct {
	Value   string `json:"value"`
	Display string `json:"display"`
	Ref     string `json:"$ref"`
}

type scimGroupRequest struct {
	Schemas     []string        `json:"schemas"`
	ExternalID  string          `json:"externalId"`
	DisplayName string          `json:"displayName"`
	Members     []scimMemberRef `json:"members"`
}

type scimMemberRef struct {
	Value   string `json:"value"`
	Display string `json:"display"`
	Ref     string `json:"$ref"`
}

type scimPatchRequest struct {
	Schemas    []string      `json:"schemas"`
	Operations []scimPatchOp `json:"Operations"`
}

type scimPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

func userSchema() map[string]interface{} {
	return map[string]interface{}{
		"id":          "urn:ietf:params:scim:schemas:core:2.0:User",
		"name":        "User",
		"description": "Core User",
		"attributes": []map[string]interface{}{
			{"name": "userName", "type": "string", "required": true, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "displayName", "type": "string", "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "externalId", "type": "string", "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "active", "type": "boolean", "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "name", "type": "complex", "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "groups", "type": "complex", "multiValued": true, "required": false, "mutability": "readOnly", "returned": "default"},
		},
	}
}

func groupSchema() map[string]interface{} {
	return map[string]interface{}{
		"id":          "urn:ietf:params:scim:schemas:core:2.0:Group",
		"name":        "Group",
		"description": "Core Group",
		"attributes": []map[string]interface{}{
			{"name": "displayName", "type": "string", "required": true, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "externalId", "type": "string", "required": false, "mutability": "readWrite", "returned": "default"},
			{"name": "members", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default"},
		},
	}
}
