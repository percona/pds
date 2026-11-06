/**
 * A username is a DNS label that starts with a letter. The adapter checks it again, so this only
 * saves a round trip on an obvious typo.
 */
export const loginNamePattern = /^[a-z]([-a-z0-9]*[a-z0-9])?$/;
