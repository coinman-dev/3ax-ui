you can't install fail2ban on windows
we don't have bash menu for windows
if you forgot your password you need to check your database with https://sqlitebrowser.org/
the app need to be open all the time

default setting:
http://localhost:2053/
user: admin
pass: admin
port: 2053


to create a self-signed certificate you need OpenSSL for Windows:
download it from https://slproweb.com/products/Win32OpenSSL.html (Win64 OpenSSL Light),
then run:

openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout localhost.key -out localhost.crt
