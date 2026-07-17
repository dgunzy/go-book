package identity

import "fmt"

type Permission string

const (
	PermissionUseMemberArea           Permission = "use_member_area"
	PermissionSubmitResult            Permission = "submit_result"
	PermissionConfirmResult           Permission = "confirm_result"
	PermissionPlaceWager              Permission = "place_wager"
	PermissionManageResults           Permission = "manage_results"
	PermissionManageMarkets           Permission = "manage_markets"
	PermissionSettleManualMarket      Permission = "settle_manual_market"
	PermissionManageMedia             Permission = "manage_media"
	PermissionIssueInvitation         Permission = "issue_invitation"
	PermissionManageMembers           Permission = "manage_members"
	PermissionMakeFinancialCorrection Permission = "make_financial_correction"
	PermissionManageRoles             Permission = "manage_roles"
)

func Authorize(principal Principal, permission Permission) error {
	if err := principal.Validate(); err != nil {
		if err == ErrSignInNotAllowed {
			return ErrUnauthenticated
		}
		return fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}

	allowed := false
	switch principal.Membership.Role {
	case RoleMember:
		allowed = isMemberPermission(permission)
	case RoleAdmin:
		allowed = isMemberPermission(permission) || isAdminPermission(permission)
	case RoleOwner:
		allowed = isMemberPermission(permission) || isAdminPermission(permission) ||
			permission == PermissionMakeFinancialCorrection || permission == PermissionManageRoles
	}
	if !allowed {
		return fmt.Errorf("%w: role %q lacks permission %q", ErrUnauthorized, principal.Membership.Role, permission)
	}
	return nil
}

func isMemberPermission(permission Permission) bool {
	switch permission {
	case PermissionUseMemberArea, PermissionSubmitResult, PermissionConfirmResult, PermissionPlaceWager:
		return true
	default:
		return false
	}
}

func isAdminPermission(permission Permission) bool {
	switch permission {
	case PermissionManageResults, PermissionManageMarkets, PermissionSettleManualMarket,
		PermissionManageMedia, PermissionIssueInvitation, PermissionManageMembers:
		return true
	default:
		return false
	}
}
