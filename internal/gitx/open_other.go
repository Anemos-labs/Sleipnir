//go:build !unix

package gitx

// noWaitFlag is zero where opens have no non-blocking mode; openRegular's
// Lstat check refuses special files there before they are opened.
const noWaitFlag = 0

// noFollowFlag is zero where the platform has no O_NOFOLLOW; openRegular refuses
// a symlink with Lstat instead.
const noFollowFlag = 0
