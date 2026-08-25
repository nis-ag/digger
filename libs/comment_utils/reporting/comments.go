package reporting

func openTag(open bool) string {
	if open {
		return ` open="true"`
	}
	return ""
}

func GetTerraformOutputAsCollapsibleComment(summary string, open bool) func(string) string {
	return func(comment string) string {
		fence := terraformFence(comment)
		return "<details" + openTag(open) + "><summary>" + summary + "</summary>\n\n" +
			fence + terraformFenceLanguage + "\n" + comment + "\n" + fence + "\n</details>"
	}
}

func GetTerraformOutputAsComment(summary string) func(string) string {
	return func(comment string) string {
		fence := terraformFence(comment)
		return summary + "\n" + fence + terraformFenceLanguage + "\n" + comment + "\n" + fence
	}
}

func AsCollapsibleComment(summary string, open bool) func(string) string {
	return func(comment string) string {
		return "<details" + openTag(open) + "><summary>" + summary + "</summary>\n" + comment + "\n</details>"
	}
}

func AsComment(summary string) func(string) string {
	return func(comment string) string {
		return summary + "\n" + comment
	}
}
