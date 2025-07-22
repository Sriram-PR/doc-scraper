import argparse
import difflib
from pathlib import Path


def get_relative_items(root_dir: Path) -> set[Path]:
    """
    Recursively finds all files and directories within root_dir
    and returns their paths relative to root_dir.
    """
    items = set()
    for item in root_dir.rglob("*"):
        items.add(item.relative_to(root_dir))
    return items


def compare_files_line_by_line(file1: Path, file2: Path) -> list[str] | None:
    """
    Compares two files line by line and returns a unified diff list
    if they differ, or None if they are identical or an error occurs.
    """
    try:
        with (
            open(file1, "r", encoding="utf-8", errors="ignore") as f1,
            open(file2, "r", encoding="utf-8", errors="ignore") as f2,
        ):
            lines1 = f1.readlines()
            lines2 = f2.readlines()

        diff = list(
            difflib.unified_diff(
                lines1, lines2, fromfile=str(file1), tofile=str(file2), lineterm="\n"
            )
        )

        if not diff:
            return None
        return diff

    except OSError as e:
        print(f"  [Error] Cannot read/compare file: {e}")
        return ["Error reading file."]
    except Exception as e:
        print(f"  [Error] Unexpected error comparing {file1} and {file2}: {e}")
        return ["Unexpected error during comparison."]


def compare_folders(dir1: Path, dir2: Path):
    """
    Compares two directories thoroughly: structure and file content.
    """
    print(f"Comparing '{dir1}' and '{dir2}'...\n")

    if not dir1.is_dir():
        print(f"Error: Folder '{dir1}' does not exist or is not a directory.")
        return
    if not dir2.is_dir():
        print(f"Error: Folder '{dir2}' does not exist or is not a directory.")
        return

    print("Scanning directories...")
    items1 = get_relative_items(dir1)
    items2 = get_relative_items(dir2)
    print(f"Found {len(items1)} items in '{dir1}', {len(items2)} items in '{dir2}'.")

    # --- 1. Structure Differences ---
    only_in_dir1 = items1 - items2
    only_in_dir2 = items2 - items1
    common_items = items1 & items2

    found_diff = False

    if only_in_dir1:
        found_diff = True
        print("\n--- Items only in '{}' ---".format(dir1))
        for item in sorted(list(only_in_dir1)):
            item_type = "(Dir)" if (dir1 / item).is_dir() else "(File)"
            print(f"+ {item} {item_type}")

    if only_in_dir2:
        found_diff = True
        print("\n--- Items only in '{}' ---".format(dir2))
        for item in sorted(list(only_in_dir2)):
            item_type = "(Dir)" if (dir2 / item).is_dir() else "(File)"
            print(f"+ {item} {item_type}")

    # --- 2. Compare Common Items ---
    if common_items:
        print(f"\n--- Comparing {len(common_items)} common items ---")
        for item_rel_path in sorted(list(common_items)):
            path1 = dir1 / item_rel_path
            path2 = dir2 / item_rel_path

            is_dir1 = path1.is_dir()
            is_dir2 = path2.is_dir()

            if is_dir1 != is_dir2:
                found_diff = True
                print(f"\n* Type mismatch: '{item_rel_path}'")
                print(f"  '{path1}': {'Directory' if is_dir1 else 'File'}")
                print(f"  '{path2}': {'Directory' if is_dir2 else 'File'}")
                continue

            if is_dir1 and is_dir2:
                # print(f"  Directory: '{item_rel_path}' (present in both)")
                pass

            elif not is_dir1 and not is_dir2:
                try:
                    stat1 = path1.stat()
                    stat2 = path2.stat()

                    if stat1.st_size != stat2.st_size:
                        found_diff = True
                        print(f"\n* Size difference: '{item_rel_path}'")
                        print(f"  '{path1}': {stat1.st_size} bytes")
                        print(f"  '{path2}': {stat2.st_size} bytes")
                        # Still attempt line-by-line comparison unless files are huge
                        # if abs(stat1.st_size - stat2.st_size) > SOME_THRESHOLD: continue

                    diff_lines = compare_files_line_by_line(path1, path2)

                    if diff_lines:
                        found_diff = True
                        print(f"\n* Content difference: '{item_rel_path}'")
                        # for line in diff_lines[:50]:
                        for line in diff_lines:
                            print(f"  {line.rstrip()}")

                except OSError as e:
                    found_diff = True
                    print(f"\n* Error accessing file stats for '{item_rel_path}': {e}")
                except Exception as e:
                    found_diff = True
                    print(
                        f"\n* Unexpected error processing file '{item_rel_path}': {e}"
                    )

    # --- 3. Final Summary ---
    print("\n--- Comparison Summary ---")
    if not found_diff:
        print("No differences found.")
    else:
        print("Differences found (listed above).")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Compare two folders thoroughly (structure and file content)."
    )
    parser.add_argument("folder1", type=Path, help="Path to the first folder.")
    parser.add_argument("folder2", type=Path, help="Path to the second folder.")

    args = parser.parse_args()

    compare_folders(args.folder1, args.folder2)
