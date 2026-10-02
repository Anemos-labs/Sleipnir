package input

import "strings"

// RoleModels completes the arguments of "/roles ROLE=MODEL ...". A word with no "=" yet completes to a role (the dim words say what
// it runs on now) and is inserted with its "=", after which the menu of models opens at once; the word after the "=" completes to
// a model, from the same list and by the same match as Choices. A word whose role is not one of the roles gets nothing.
func RoleModels(command string, roles, models func() []Choice) Completer {
	prefix := "/" + strings.TrimPrefix(command, "/") + " "
	return CompleterFunc(func(line string, cursor int) (int, []Candidate) {
		if cursor < len(prefix) || cursor > len(line) || !strings.HasPrefix(line, prefix) || strings.ContainsAny(line[:cursor], "\U0000fffc") {
			return 0, nil
		}
		start := strings.LastIndexByte(line[:cursor], ' ') + 1
		role, typed, assigned := strings.Cut(line[start:cursor], "=")
		if !assigned {
			return start, matchChoices(roles(), strings.Fields(role), "=")
		}
		for _, r := range roles() {
			if r.Text == role {
				return start + len(role) + 1, matchChoices(models(), strings.Fields(typed), " ")
			}
		}
		return 0, nil
	})
}
