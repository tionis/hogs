package scim

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
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
		users, err = filterUsers(users, filter)
		if err != nil {
			scimError(w, 400, "invalidFilter", err.Error())
			return
		}
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
		scimError(w, 400, "invalidValue", "userName is required")
		return
	}
	if strings.TrimSpace(req.ExternalID) == "" {
		scimError(w, 400, "invalidValue", "externalId is required for Authentik identity correlation")
		return
	}

	existing, err := h.Store.GetUserByExternalID(req.ExternalID)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if existing == nil {
		existing, err = h.Store.GetUserByOIDCSubject(req.ExternalID)
		if err != nil {
			scimError(w, 500, "InternalServerError", err.Error())
			return
		}
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

	user := existing
	if user == nil {
		user, err = h.Store.CreateUser(userName, "user")
		if err != nil {
			scimError(w, 409, "uniqueness", err.Error())
			return
		}
	}

	if err := h.Store.UpdateUserSCIMIdentity(user.ID, userName, externalID, displayName, active); err != nil {
		scimError(w, 409, "uniqueness", err.Error())
		return
	}

	user.Username = userName
	user.ExternalID = externalID
	user.DisplayName = displayName
	user.PreferredUsername = userName
	user.Active = active

	if req.Groups != nil {
		if err := h.syncUserGroups(user.ID, req.Groups); err != nil {
			scimError(w, 400, "invalidValue", err.Error())
			return
		}
		h.recalcUserRole(user)
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

	if strings.TrimSpace(req.UserName) == "" || strings.TrimSpace(req.ExternalID) == "" {
		scimError(w, 400, "invalidValue", "userName and externalId are required")
		return
	}
	if user.ExternalID != "" && user.ExternalID != req.ExternalID {
		scimError(w, 409, "uniqueness", "externalId is immutable")
		return
	}
	if err := h.Store.UpdateUserSCIMIdentity(user.ID, req.UserName, req.ExternalID, displayName, active); err != nil {
		scimError(w, 409, "uniqueness", err.Error())
		return
	}

	user.Username = req.UserName
	user.ExternalID = req.ExternalID
	user.DisplayName = displayName
	user.PreferredUsername = req.UserName
	user.Active = active

	if req.Groups != nil {
		if err := h.syncUserGroups(user.ID, req.Groups); err != nil {
			scimError(w, 400, "invalidValue", err.Error())
			return
		}
		h.recalcUserRole(user)
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
	username := user.Username
	externalID := user.ExternalID
	displayName := user.DisplayName
	active := user.Active

	for _, op := range req.Operations {
		switch strings.ToLower(op.Op) {
		case "replace":
			switch strings.ToLower(op.Path) {
			case "active":
				if value, ok := op.Value.(bool); ok {
					active = value
					needsSessionInvalidate = true
				}
			case "displayname":
				if dn, ok := op.Value.(string); ok {
					displayName = dn
				}
			case "externalid":
				if eid, ok := op.Value.(string); ok {
					if externalID != "" && externalID != eid {
						scimError(w, 409, "uniqueness", "externalId is immutable")
						return
					}
					externalID = eid
				}
			case "username":
				if name, ok := op.Value.(string); ok {
					username = name
				}
			default:
				if op.Path == "" && op.Value != nil {
					if m, ok := op.Value.(map[string]interface{}); ok {
						if value, ok := m["active"].(bool); ok {
							active = value
							needsSessionInvalidate = true
						}
						if dn, ok := m["displayName"].(string); ok {
							displayName = dn
						}
						if name, ok := m["userName"].(string); ok {
							username = name
						}
						if eid, ok := m["externalId"].(string); ok {
							if externalID != "" && externalID != eid {
								scimError(w, 409, "uniqueness", "externalId is immutable")
								return
							}
							externalID = eid
						}
					}
				}
			}
		case "add":
			if strings.EqualFold(op.Path, "groups") {
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
			if strings.EqualFold(op.Path, "active") {
				active = false
				needsSessionInvalidate = true
			} else if value, ok := scimPathValue(op.Path, "groups"); ok {
				if gid, err := strconv.Atoi(value); err == nil {
					h.Store.RemoveSCIMGroupMember(gid, user.ID)
					needsSessionInvalidate = true
				}
			}
		}
	}

	if strings.TrimSpace(username) == "" || strings.TrimSpace(externalID) == "" {
		scimError(w, 400, "invalidValue", "userName and externalId are required")
		return
	}
	if err := h.Store.UpdateUserSCIMIdentity(user.ID, username, externalID, displayName, active); err != nil {
		scimError(w, 409, "uniqueness", err.Error())
		return
	}
	user.Username = username
	user.ExternalID = externalID
	user.DisplayName = displayName
	user.PreferredUsername = username
	user.Active = active

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
	filter := r.URL.Query().Get("filter")

	groups, err := h.Store.ListSCIMGroups()
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	if filter != "" {
		groups, err = filterGroups(groups, filter)
		if err != nil {
			scimError(w, 400, "invalidFilter", err.Error())
			return
		}
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
	if strings.TrimSpace(req.ExternalID) == "" {
		scimError(w, 400, "invalidValue", "externalId is required for Authentik group correlation")
		return
	}

	existing, err := h.Store.GetSCIMGroupByExternalID(req.ExternalID)
	if existing != nil {
		scimError(w, 409, "uniqueness", "Group already exists")
		return
	}
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	existing, err = h.Store.GetSCIMGroupByName(req.DisplayName)
	if err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}

	group := existing
	if group == nil {
		group = &database.SCIMGroup{
			ExternalID:  req.ExternalID,
			DisplayName: req.DisplayName,
		}
		if err := h.Store.CreateSCIMGroup(group); err != nil {
			scimError(w, 500, "InternalServerError", err.Error())
			return
		}
	} else {
		if group.ExternalID != "" && group.ExternalID != req.ExternalID {
			scimError(w, 409, "uniqueness", "displayName belongs to a different Authentik group")
			return
		}
		if err := h.Store.UpdateSCIMGroup(group.ID, req.ExternalID, req.DisplayName); err != nil {
			scimError(w, 500, "InternalServerError", err.Error())
			return
		}
		group.ExternalID = req.ExternalID
	}

	userIDs, err := h.memberIDs(req.Members)
	if err != nil {
		scimError(w, 400, "invalidValue", err.Error())
		return
	}
	if err := h.Store.SetSCIMGroupMembers(group.ID, userIDs); err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	h.recalculateUsers(userIDs)

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
	if strings.TrimSpace(req.DisplayName) == "" || strings.TrimSpace(req.ExternalID) == "" {
		scimError(w, 400, "invalidValue", "displayName and externalId are required")
		return
	}
	if group.ExternalID != "" && group.ExternalID != req.ExternalID {
		scimError(w, 409, "uniqueness", "externalId is immutable")
		return
	}
	oldMembers, _ := h.Store.GetSCIMGroupMembers(id)
	if err := h.Store.UpdateSCIMGroup(id, req.ExternalID, req.DisplayName); err != nil {
		scimError(w, 409, "uniqueness", err.Error())
		return
	}
	group.DisplayName = req.DisplayName
	group.ExternalID = req.ExternalID

	userIDs, err := h.memberIDs(req.Members)
	if err != nil {
		scimError(w, 400, "invalidValue", err.Error())
		return
	}
	if err := h.Store.SetSCIMGroupMembers(id, userIDs); err != nil {
		scimError(w, 500, "InternalServerError", err.Error())
		return
	}
	h.recalculateUsers(appendUserIDs(oldMembers, userIDs))

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
	oldMembers, _ := h.Store.GetSCIMGroupMembers(id)

	for _, op := range req.Operations {
		switch strings.ToLower(op.Op) {
		case "add":
			if strings.EqualFold(op.Path, "members") || op.Path == "" {
				if refs, ok := op.Value.([]interface{}); ok {
					for _, ref := range refs {
						if m, ok := ref.(map[string]interface{}); ok {
							if val, ok := m["value"].(string); ok {
								uid, _ := strconv.Atoi(val)
								if uid > 0 {
									if err := h.Store.AddSCIMGroupMember(id, uid); err != nil {
										scimError(w, 400, "invalidValue", err.Error())
										return
									}
								}
							}
						}
					}
				}
			}
		case "remove":
			if value, ok := scimPathValue(op.Path, "members"); ok {
				if uid, err := strconv.Atoi(value); err == nil {
					if uid > 0 {
						h.Store.RemoveSCIMGroupMember(id, uid)
					}
				}
			} else if strings.EqualFold(op.Path, "members") {
				refs, hasRefs := op.Value.([]interface{})
				if !hasRefs {
					h.Store.SetSCIMGroupMembers(id, nil)
					break
				}
				for _, ref := range refs {
					item, ok := ref.(map[string]interface{})
					if !ok {
						continue
					}
					value, ok := item["value"].(string)
					if !ok {
						continue
					}
					if uid, err := strconv.Atoi(value); err == nil && uid > 0 {
						h.Store.RemoveSCIMGroupMember(id, uid)
					}
				}
			}
		case "replace":
			if op.Path == "" {
				attributes, ok := op.Value.(map[string]interface{})
				if !ok {
					scimError(w, 400, "invalidValue", "replace without a path requires an object")
					return
				}
				displayName := group.DisplayName
				if value, ok := attributes["displayName"].(string); ok {
					displayName = value
				}
				externalID := group.ExternalID
				if value, ok := attributes["externalId"].(string); ok {
					if strings.TrimSpace(value) == "" {
						scimError(w, 400, "invalidValue", "externalId must be a non-empty string")
						return
					}
					if group.ExternalID != "" && group.ExternalID != value {
						scimError(w, 409, "uniqueness", "externalId is immutable")
						return
					}
					externalID = value
				}
				if err := h.Store.UpdateSCIMGroup(id, externalID, displayName); err != nil {
					scimError(w, 409, "uniqueness", err.Error())
					return
				}
				group.DisplayName = displayName
				group.ExternalID = externalID
			} else if strings.EqualFold(op.Path, "displayName") {
				if dn, ok := op.Value.(string); ok {
					if err := h.Store.UpdateSCIMGroup(id, group.ExternalID, dn); err != nil {
						scimError(w, 409, "uniqueness", err.Error())
						return
					}
					group.DisplayName = dn
				}
			} else if strings.EqualFold(op.Path, "externalId") {
				externalID, ok := op.Value.(string)
				if !ok || strings.TrimSpace(externalID) == "" {
					scimError(w, 400, "invalidValue", "externalId must be a non-empty string")
					return
				}
				if group.ExternalID != "" && group.ExternalID != externalID {
					scimError(w, 409, "uniqueness", "externalId is immutable")
					return
				}
				if err := h.Store.UpdateSCIMGroup(id, externalID, group.DisplayName); err != nil {
					scimError(w, 409, "uniqueness", err.Error())
					return
				}
				group.ExternalID = externalID
			} else if strings.EqualFold(op.Path, "members") {
				refs, _ := op.Value.([]interface{})
				var userIDs []int
				for _, ref := range refs {
					if item, ok := ref.(map[string]interface{}); ok {
						if value, ok := item["value"].(string); ok {
							if uid, err := strconv.Atoi(value); err == nil && uid > 0 {
								userIDs = append(userIDs, uid)
							}
						}
					}
				}
				if err := h.Store.SetSCIMGroupMembers(id, userIDs); err != nil {
					scimError(w, 400, "invalidValue", err.Error())
					return
				}
			}
		}
	}

	currentMembers, _ := h.Store.GetSCIMGroupMembers(id)
	var currentIDs []int
	for _, member := range currentMembers {
		currentIDs = append(currentIDs, member.ID)
	}
	h.recalculateUsers(appendUserIDs(oldMembers, currentIDs))

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
		"userName": u.Username,
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

func (h *Handler) syncUserGroups(userID int, groupRefs []scimGroupRef) error {
	var groupIDs []int
	for _, ref := range groupRefs {
		gid, err := strconv.Atoi(ref.Value)
		if err != nil || gid <= 0 {
			return fmt.Errorf("invalid group reference %q", ref.Value)
		}
		groupIDs = append(groupIDs, gid)
	}
	current, err := h.Store.GetSCIMGroupsForUser(userID)
	if err != nil {
		return err
	}
	desired := make(map[int]bool, len(groupIDs))
	for _, gid := range groupIDs {
		desired[gid] = true
		if err := h.Store.AddSCIMGroupMember(gid, userID); err != nil {
			return err
		}
	}
	for _, group := range current {
		if !desired[group.ID] {
			if err := h.Store.RemoveSCIMGroupMember(group.ID, userID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) memberIDs(refs []scimMemberRef) ([]int, error) {
	userIDs := make([]int, 0, len(refs))
	for _, ref := range refs {
		uid, err := strconv.Atoi(ref.Value)
		if err != nil || uid <= 0 {
			return nil, fmt.Errorf("invalid member reference %q", ref.Value)
		}
		user, err := h.Store.GetUserByID(uid)
		if err != nil {
			return nil, err
		}
		if user == nil {
			return nil, fmt.Errorf("member user %d does not exist", uid)
		}
		userIDs = append(userIDs, uid)
	}
	return userIDs, nil
}

func appendUserIDs(users []database.User, ids []int) []int {
	seen := make(map[int]bool, len(users)+len(ids))
	result := make([]int, 0, len(users)+len(ids))
	for _, user := range users {
		if !seen[user.ID] {
			seen[user.ID] = true
			result = append(result, user.ID)
		}
	}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func (h *Handler) recalculateUsers(userIDs []int) {
	seen := make(map[int]bool, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 || seen[userID] {
			continue
		}
		seen[userID] = true
		user, err := h.Store.GetUserByID(userID)
		if err != nil || user == nil {
			continue
		}
		h.recalcUserRole(user)
		h.triggerSessionInvalidation(user)
	}
}

func (h *Handler) triggerSessionInvalidation(user *database.User) {
	if h.Auth == nil {
		return
	}

	if user.ExternalID != "" {
		if err := h.Store.DeleteSessionsBySub(user.ExternalID); err != nil {
			log.Printf("SCIM: failed to invalidate sessions for user %s: %v", user.Username, err)
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

func filterUsers(users []database.User, filter string) ([]database.User, error) {
	attribute, value, ok := parseEqualityFilter(filter)
	if !ok {
		return nil, fmt.Errorf("only exact userName, displayName, and externalId filters are supported")
	}
	var result []database.User
	for _, user := range users {
		matches := false
		switch strings.ToLower(attribute) {
		case "username":
			matches = strings.EqualFold(user.Username, value)
		case "externalid":
			matches = user.ExternalID == value
		case "displayname":
			matches = user.DisplayName == value
		default:
			return nil, fmt.Errorf("unsupported user filter attribute %q", attribute)
		}
		if matches {
			result = append(result, user)
		}
	}
	return result, nil
}

func filterGroups(groups []database.SCIMGroup, filter string) ([]database.SCIMGroup, error) {
	attribute, value, ok := parseEqualityFilter(filter)
	if !ok {
		return nil, fmt.Errorf("only exact userName, displayName, and externalId filters are supported")
	}
	var result []database.SCIMGroup
	for _, group := range groups {
		matches := false
		switch strings.ToLower(attribute) {
		case "displayname":
			matches = group.DisplayName == value
		case "externalid":
			matches = group.ExternalID == value
		default:
			return nil, fmt.Errorf("unsupported group filter attribute %q", attribute)
		}
		if matches {
			result = append(result, group)
		}
	}
	return result, nil
}

var equalityFilterPattern = regexp.MustCompile(`(?i)^\s*(userName|displayName|externalId)\s+eq\s+"([^"]*)"\s*$`)
var pathValuePattern = regexp.MustCompile(`(?i)^\s*([A-Za-z]+)\s*\[\s*value\s+eq\s+"([^"]+)"\s*\]\s*$`)

func parseEqualityFilter(filter string) (attribute, value string, ok bool) {
	parts := equalityFilterPattern.FindStringSubmatch(filter)
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func scimPathValue(path, attribute string) (string, bool) {
	parts := pathValuePattern.FindStringSubmatch(path)
	if len(parts) != 3 || !strings.EqualFold(parts[1], attribute) {
		return "", false
	}
	return parts[2], true
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
