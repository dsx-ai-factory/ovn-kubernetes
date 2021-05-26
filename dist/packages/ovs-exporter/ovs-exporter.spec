Name: ovs-exporter
Version: 1.1.0
Release: 1
Summary: Standalone OVS Exporter

License: ASL 2.0
Source0: ovs-exporter.tar.gz

BuildArch: x86_64

Requires: openssl

%global debug_package %{nil}

%define config_dir /etc/ovn-kube-util
%define tls_dir %{config_dir}/tls

%description
%{summary}

%prep
%setup -q -n ovs-exporter

%install
mkdir -p %{buildroot}/
install -p -m 0755 -D bin/ovn-kube-util %{buildroot}/%{_bindir}/ovn-kube-util
install -p -m 0644 -D bin/git_info %{buildroot}/%{config_dir}/git_info
install -p -m 0644 -D config/ovs-exporter.service %{buildroot}/etc/systemd/system/ovs-exporter.service
sed -i 's|<EXPORTER_PATH>|%{_bindir}|' %{buildroot}/etc/systemd/system/ovs-exporter.service
sed -i 's|<TLS_PATH>|%{tls_dir}|g' %{buildroot}/etc/systemd/system/ovs-exporter.service
sed -i 's/<LISTENING_IP>/0.0.0.0/' %{buildroot}/etc/systemd/system/ovs-exporter.service

%files
%{_bindir}/ovn-kube-util
%{config_dir}/git_info
/etc/systemd/system/ovs-exporter.service

%pre
mkdir -p -m 0700 %{tls_dir}
chown root.root %{tls_dir}
openssl req -x509 -nodes -newkey rsa:4096 -keyout %{tls_dir}/key.pem -out %{tls_dir}/cert.pem -days 365 -subj '/CN=*.nvmetal.net'

%post
systemctl daemon-reload
systemctl enable ovs-exporter
systemctl start ovs-exporter

%preun
systemctl stop ovs-exporter
systemctl disable ovs-exporter

%postun
systemctl daemon-reload
rm -rf %{config_dir}
