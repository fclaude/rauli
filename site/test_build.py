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
