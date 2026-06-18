' Chay start-tailscale.bat HOAN TOAN AN: khong cua so cmd, khong nhay den.
' Duoc Scheduled Task (ONLOGON) goi qua wscript.exe -> tu chay luc dang nhap
' Windows ma KHONG hien thi gi het.
'
' LAN DAU dang nhap Google: chay start-itop.bat (hoac start-tailscale.bat) truc
' tiep de THAY URL dang nhap. Cac lan sau task nay tu ket noi lai (state da luu),
' khong can trinh duyet -> dung nghia "chi lan dau can login".
Option Explicit
Dim fso, baseDir, sh
Set fso = CreateObject("Scripting.FileSystemObject")
baseDir = fso.GetParentFolderName(WScript.ScriptFullName)
Set sh = CreateObject("WScript.Shell")
' Tham so: 0 = cua so an hoan toan ; False = khong cho tien trinh ket thuc.
sh.Run """" & baseDir & "\start-tailscale.bat"" auto", 0, False
