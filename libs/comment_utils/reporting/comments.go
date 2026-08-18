package reporting

func openTag(open bool) string {
	if open {
		return ` open="true"`
	}
	return ""
}

func GetTerraformOutputAsCollapsibleComment(summary string, open bool) func(string) string {
	return func(comment string) string {
		return "<details" + openTag(open) + "><summary>" + summary + "</summary>\n\n" +
			"```terraform\n" + comment + "\n```\n</details>"
	}
}

func GetTerraformOutputAsComment(summary string) func(string) string {
	return func(comment string) string {
		return summary + "\n```terraform\n" + comment + "\n```"
	}
}

func AsCollapsibleComment(summary string, open bool) func(string) string {
	return func(comment string) string {
		return "<details" + openTag(open) + "><summary>" + summary + "</summary>\n  " + comment + "\n</details>"
	}
}

func AsComment(summary string) func(string) string {
	return func(comment string) string {
		return summary + "\n" + comment
	}
}
