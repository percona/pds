const fallback = "Signing in did not finish. Try again.";

/**
 * The sentence for a code the control plane puts in the sign in page's address
 * after a sign in it refused. The control plane decides which gate refused and
 * the page decides the words, so nothing here says more than the code does.
 * `invited` is the masked address an invitation was sent to.
 */
export function signInErrorMessage(code: string, invited: string | null): string {
  const address = invited ? ` ${invited}` : "";

  switch (code) {
    case "no_membership":
      return "This account does not belong to an organization yet. Ask an admin to invite you, or create an organization.";
    case "email_unverified":
      return "Your identity provider has not verified your email address. Verify it there, then try again.";
    case "signup_rate_limited":
      return "Too many organizations were created from this address. Try again in an hour.";
    case "signup_unavailable":
      return "New organizations cannot be created right now. Contact Percona support.";
    case "signup_limit_reached":
      return "You have created as many organizations as one person can. Contact Percona support.";
    case "organization_name_invalid":
      return "Give the organization a name of up to 200 characters.";
    case "invite_invalid":
      return "That invite link is not valid. Ask your organization admin for a new one.";
    case "invite_spent":
      return "That invite link has already been used. Ask your organization admin for a new one.";
    case "invite_expired":
      return "That invite link has expired. Ask your organization admin for a new one.";
    case "invite_already_member":
      return "You already belong to this organization. Sign in to open it.";
    case "invite_email_mismatch":
      return invited
        ? `This invitation was sent to${address}. Sign in with that account.`
        : "This invitation was sent to a different email address. Sign in with that account.";
    case "invite_email_unverified":
      return invited
        ? `Verify${address} with your identity provider, then follow the invite link again.`
        : "Verify your email address with your identity provider, then follow the invite link again.";
    default:
      return fallback;
  }
}
