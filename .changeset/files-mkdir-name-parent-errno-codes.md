---
default: patch
---

`files.mkdir` makes its folder with mode 0777 under the umask and refuses a link at or above the parent. It gives a code with the errno for each OS error, allows a leading space in a name and refuses more names.
