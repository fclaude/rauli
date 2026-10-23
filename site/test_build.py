"""Tests for site/build.py. Run with: python3 -m unittest discover -s site"""
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import build  # noqa: E402

REFERENCE = "### `navigate`\n\nOpen a page.\n\n### `click`\n\nClick.\n"


class ReferenceCheck(unittest.TestCase):
    def test_a_matching_reference_passes(self):
        build.check_reference(["click", "navigate"], REFERENCE)

    def test_a_tool_without_an_entry_stops_the_build(self):
        with self.assertRaisesRegex(build.BuildError, "no entry in the reference: snapshot"):
            build.check_reference(["click", "navigate", "snapshot"], REFERENCE)

    def test_an_entry_for_a_removed_tool_stops_the_build(self):
        with self.assertRaisesRegex(build.BuildError, "no longer exist: click"):
            build.check_reference(["navigate"], REFERENCE)


COMMAND_OUTPUT = """# Tool reference

An introduction the page does not need.

## click

Click an element.

| Argument | Type | Required | What it does |
| --- | --- | --- | --- |
| `ref` | string | yes | The element, such as e12. |
| `double` | boolean | no | Double-click, or `a\\|b`. |

## back

Go back.

It takes no arguments.
"""


class Reshape(unittest.TestCase):
    def test_tools_move_down_a_level_and_the_preface_goes(self):
        out = build.reshape_reference(COMMAND_OUTPUT)
        self.assertNotIn("# Tool reference", out)
        self.assertNotIn("An introduction", out)
        self.assertIn("### `click`", out)
        self.assertIn("### `back`", out)
        self.assertEqual(build.TOOL_HEADING.findall(out), ["click", "back"])

    def test_required_folds_into_the_type_column(self):
        out = build.reshape_reference(COMMAND_OUTPUT)
        self.assertIn("| Argument | Type | What it does |", out)
        self.assertIn("| `ref` | string, required | The element, such as e12. |", out)
        self.assertIn("| `double` | boolean | Double-click, or `a\\|b`. |", out)

    def test_a_table_shape_it_does_not_know_stops_the_build(self):
        with self.assertRaisesRegex(build.BuildError, "shape this script does not know"):
            build.reshape_reference("\n## x\n\n| Argument | Type |\n| --- | --- |\n| `a` | string |\n")

    def test_output_without_tool_headings_stops_the_build(self):
        with self.assertRaisesRegex(build.BuildError, "no tool headings"):
            build.reshape_reference("# Tool reference\n\nNothing here.\n")


class Links(unittest.TestCase):
    def test_a_link_to_another_page_becomes_a_site_link(self):
        self.assertEqual(build.rewrite("tools.md", "docs/home.md"), "@/tools.md")
        self.assertEqual(build.rewrite("security.md#what-the-guard-sees", "docs/tools.md"), "@/security.md#what-the-guard-sees")

    def test_a_link_to_a_file_becomes_a_link_to_the_repository(self):
        self.assertEqual(build.rewrite("examples/readpage/main.go", "docs/getting-started.md"), build.REPO + "docs/examples/readpage/main.go")

    def test_outside_links_are_left_alone(self):
        self.assertEqual(build.rewrite("https://example.com/x", "docs/home.md"), "https://example.com/x")


class Markers(unittest.TestCase):
    def test_an_include_of_a_missing_file_stops_the_build(self):
        with self.assertRaisesRegex(build.BuildError, "cannot include docs/nothing.go"):
            build.fill_markers(["<!-- include: docs/nothing.go go -->"], "docs/x.md", "")

    def test_the_tools_marker_is_replaced_by_the_reference(self):
        out = build.fill_markers(["before", build.TOOLS_MARKER, "after"], "docs/tools.md", REFERENCE.rstrip("\n"))
        self.assertIn("### `navigate`", "\n".join(out))
        self.assertNotIn(build.TOOLS_MARKER, out)


if __name__ == "__main__":
    unittest.main()
