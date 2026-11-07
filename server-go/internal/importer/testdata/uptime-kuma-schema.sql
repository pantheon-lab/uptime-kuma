-- Created by a real boot of the Node.js backend on MariaDB, at commit d1788343.

CREATE TABLE `monitor` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(150) DEFAULT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `interval` int(11) NOT NULL DEFAULT 20,
  `url` text DEFAULT NULL,
  `type` varchar(20) DEFAULT NULL,
  `weight` int(11) DEFAULT 2000,
  `hostname` varchar(255) DEFAULT NULL,
  `port` int(11) DEFAULT NULL,
  `created_date` datetime NOT NULL DEFAULT current_timestamp(),
  `keyword` varchar(255) DEFAULT NULL,
  `maxretries` int(11) NOT NULL DEFAULT 0,
  `ignore_tls` tinyint(1) NOT NULL DEFAULT 0,
  `upside_down` tinyint(1) NOT NULL DEFAULT 0,
  `maxredirects` int(11) NOT NULL DEFAULT 10,
  `accepted_statuscodes_json` text NOT NULL DEFAULT '["200-299"]',
  `dns_resolve_type` varchar(5) DEFAULT NULL,
  `dns_resolve_server` varchar(255) DEFAULT NULL,
  `dns_last_result` text DEFAULT NULL,
  `retry_interval` int(11) NOT NULL DEFAULT 0,
  `push_token` varchar(32) DEFAULT NULL,
  `method` text NOT NULL DEFAULT 'GET',
  `body` text DEFAULT NULL,
  `headers` text DEFAULT NULL,
  `basic_auth_user` text DEFAULT NULL,
  `basic_auth_pass` text DEFAULT NULL,
  `docker_host` int(10) unsigned DEFAULT NULL,
  `docker_container` varchar(255) DEFAULT NULL,
  `proxy_id` int(10) unsigned DEFAULT NULL,
  `expiry_notification` tinyint(1) DEFAULT 1,
  `mqtt_topic` text DEFAULT NULL,
  `mqtt_success_message` varchar(255) DEFAULT NULL,
  `mqtt_username` varchar(255) DEFAULT NULL,
  `mqtt_password` varchar(255) DEFAULT NULL,
  `database_connection_string` varchar(2000) DEFAULT NULL,
  `database_query` text DEFAULT NULL,
  `auth_method` varchar(250) DEFAULT NULL,
  `auth_domain` text DEFAULT NULL,
  `auth_workstation` text DEFAULT NULL,
  `grpc_url` varchar(255) DEFAULT NULL,
  `grpc_protobuf` text DEFAULT NULL,
  `grpc_body` text DEFAULT NULL,
  `grpc_metadata` text DEFAULT NULL,
  `grpc_method` text DEFAULT NULL,
  `grpc_service_name` text DEFAULT NULL,
  `grpc_enable_tls` tinyint(1) NOT NULL DEFAULT 0,
  `radius_username` varchar(255) DEFAULT NULL,
  `radius_password` varchar(255) DEFAULT NULL,
  `radius_calling_station_id` varchar(50) DEFAULT NULL,
  `radius_called_station_id` varchar(50) DEFAULT NULL,
  `radius_secret` varchar(255) DEFAULT NULL,
  `resend_interval` int(11) NOT NULL DEFAULT 0,
  `packet_size` int(11) NOT NULL DEFAULT 56,
  `game` varchar(255) DEFAULT NULL,
  `http_body_encoding` varchar(25) DEFAULT NULL,
  `description` text DEFAULT NULL,
  `tls_ca` text DEFAULT NULL,
  `tls_cert` text DEFAULT NULL,
  `tls_key` text DEFAULT NULL,
  `parent` int(10) unsigned DEFAULT NULL,
  `invert_keyword` tinyint(1) NOT NULL DEFAULT 0,
  `json_path` text DEFAULT NULL,
  `expected_value` varchar(255) DEFAULT NULL,
  `kafka_producer_topic` varchar(255) DEFAULT NULL,
  `kafka_producer_brokers` text DEFAULT NULL,
  `kafka_producer_ssl` tinyint(1) NOT NULL DEFAULT 0,
  `kafka_producer_allow_auto_topic_creation` tinyint(1) NOT NULL DEFAULT 0,
  `kafka_producer_sasl_options` text DEFAULT NULL,
  `kafka_producer_message` text DEFAULT NULL,
  `oauth_client_id` text DEFAULT NULL,
  `oauth_client_secret` text DEFAULT NULL,
  `oauth_token_url` text DEFAULT NULL,
  `oauth_scopes` text DEFAULT NULL,
  `oauth_auth_method` text DEFAULT NULL,
  `timeout` double NOT NULL DEFAULT 0,
  `gamedig_given_port_only` tinyint(1) NOT NULL DEFAULT 1,
  `mqtt_check_type` varchar(255) NOT NULL DEFAULT 'keyword',
  `remote_browser` int(10) unsigned DEFAULT NULL,
  `snmp_oid` varchar(255) DEFAULT NULL,
  `snmp_version` enum('1','2c','3') DEFAULT '2c',
  `json_path_operator` varchar(255) DEFAULT NULL,
  `cache_bust` tinyint(1) NOT NULL DEFAULT 0,
  `conditions` text NOT NULL DEFAULT '[]',
  `rabbitmq_nodes` text DEFAULT NULL,
  `rabbitmq_username` varchar(255) DEFAULT NULL,
  `rabbitmq_password` varchar(255) DEFAULT NULL,
  `smtp_security` varchar(255) DEFAULT NULL,
  `ws_ignore_sec_websocket_accept_header` tinyint(1) NOT NULL DEFAULT 0,
  `ws_subprotocol` varchar(255) NOT NULL DEFAULT '',
  `ping_count` int(11) NOT NULL DEFAULT 1,
  `ping_numeric` tinyint(1) NOT NULL DEFAULT 1,
  `ping_per_request_timeout` int(11) NOT NULL DEFAULT 2,
  `ip_family` varchar(4) DEFAULT NULL,
  `manual_status` smallint(6) DEFAULT NULL,
  `oauth_audience` varchar(255) DEFAULT NULL,
  `mqtt_websocket_path` varchar(255) DEFAULT NULL,
  `domain_expiry_notification` tinyint(1) DEFAULT 0,
  `save_response` tinyint(1) NOT NULL DEFAULT 0,
  `save_error_response` tinyint(1) NOT NULL DEFAULT 1,
  `response_max_length` int(11) NOT NULL DEFAULT 1024,
  `subtype` varchar(10) DEFAULT NULL,
  `location` varchar(255) DEFAULT NULL,
  `protocol` varchar(20) DEFAULT NULL,
  `snmp_v3_username` varchar(255) DEFAULT NULL,
  `expected_tls_alert` varchar(50) DEFAULT NULL,
  `retry_only_on_status_code_failure` tinyint(1) NOT NULL DEFAULT 0,
  `screenshot_delay` int(10) unsigned NOT NULL DEFAULT 0,
  `ntp_stratum_threshold` int(11) DEFAULT 5,
  `ntp_time_offset_threshold` int(11) DEFAULT 1000,
  `ntp_root_dispersion_threshold` int(11) DEFAULT 500,
  `bearer_token` text DEFAULT NULL,
  `gamedig_token` text DEFAULT NULL,
  `user_id` varchar(255) DEFAULT NULL,
  `ssh_username` varchar(255) DEFAULT NULL,
  `ssh_password` varchar(255) DEFAULT NULL,
  `sftp_path` varchar(255) DEFAULT NULL,
  `ssh_private_key` text DEFAULT NULL,
  `ssh_passphrase` varchar(255) DEFAULT NULL,
  `ssh_auth_method` varchar(255) DEFAULT 'password',
  `config` longtext NOT NULL DEFAULT '{}',
  PRIMARY KEY (`id`),
  KEY `monitor_docker_host_foreign` (`docker_host`),
  KEY `monitor_proxy_id_foreign` (`proxy_id`),
  KEY `monitor_parent_foreign` (`parent`),
  KEY `monitor_remote_browser_index` (`remote_browser`),
  KEY `monitor_user_id_foreign` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `heartbeat` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `important` tinyint(1) NOT NULL DEFAULT 0,
  `monitor_id` int(10) unsigned NOT NULL,
  `status` smallint(6) NOT NULL,
  `msg` text DEFAULT NULL,
  `time` datetime NOT NULL,
  `ping` bigint(20) DEFAULT NULL,
  `duration` int(11) NOT NULL DEFAULT 0,
  `down_count` int(11) NOT NULL DEFAULT 0,
  `end_time` datetime DEFAULT NULL,
  `retries` int(11) NOT NULL DEFAULT 0,
  `response` text DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `heartbeat_important_index` (`important`),
  KEY `monitor_time_index` (`monitor_id`,`time`),
  KEY `heartbeat_monitor_id_index` (`monitor_id`),
  KEY `monitor_important_time_index` (`monitor_id`,`important`,`time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `stat_hourly` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` int(10) unsigned NOT NULL,
  `timestamp` int(11) NOT NULL COMMENT 'Unix timestamp rounded down to the nearest hour',
  `ping` float(20,2) NOT NULL,
  `ping_min` float(20,2) NOT NULL DEFAULT 0.00,
  `ping_max` float(20,2) NOT NULL DEFAULT 0.00,
  `up` smallint(6) NOT NULL,
  `down` smallint(6) NOT NULL,
  `extras` text DEFAULT NULL COMMENT 'Extra statistics during this time period',
  PRIMARY KEY (`id`),
  UNIQUE KEY `stat_hourly_monitor_id_timestamp_unique` (`monitor_id`,`timestamp`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci COMMENT='This table contains the hourly aggregate statistics for each monitor';

CREATE TABLE `stat_daily` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` int(10) unsigned NOT NULL,
  `timestamp` int(11) NOT NULL COMMENT 'Unix timestamp rounded down to the nearest day',
  `ping` float(20,2) NOT NULL,
  `up` int(10) unsigned NOT NULL,
  `down` int(10) unsigned NOT NULL,
  `ping_min` float(20,2) NOT NULL DEFAULT 0.00,
  `ping_max` float(20,2) NOT NULL DEFAULT 0.00,
  `extras` text DEFAULT NULL COMMENT 'Extra statistics during this time period',
  PRIMARY KEY (`id`),
  UNIQUE KEY `stat_daily_monitor_id_timestamp_unique` (`monitor_id`,`timestamp`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci COMMENT='This table contains the daily aggregate statistics for each monitor';

CREATE TABLE `notification` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) DEFAULT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `user_id` int(10) unsigned DEFAULT NULL,
  `is_default` tinyint(1) NOT NULL DEFAULT 0,
  `config` longtext DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `monitor_notification` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` int(10) unsigned NOT NULL,
  `notification_id` int(10) unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `monitor_notification_notification_id_foreign` (`notification_id`),
  KEY `monitor_notification_index` (`monitor_id`,`notification_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `tag` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `color` varchar(255) NOT NULL,
  `created_date` datetime NOT NULL DEFAULT current_timestamp(),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `monitor_tag` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` int(10) unsigned NOT NULL,
  `tag_id` int(10) unsigned NOT NULL,
  `value` text DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `monitor_tag_monitor_id_foreign` (`monitor_id`),
  KEY `monitor_tag_tag_id_foreign` (`tag_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `docker_host` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int(10) unsigned NOT NULL,
  `docker_daemon` varchar(255) DEFAULT NULL,
  `docker_type` varchar(255) DEFAULT NULL,
  `name` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `remote_browser` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `url` varchar(255) NOT NULL,
  `user_id` int(10) unsigned DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `proxy` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int(10) unsigned NOT NULL,
  `protocol` varchar(10) NOT NULL,
  `host` varchar(255) NOT NULL,
  `port` smallint(6) NOT NULL,
  `auth` tinyint(1) NOT NULL,
  `username` varchar(255) DEFAULT NULL,
  `password` varchar(255) DEFAULT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `default` tinyint(1) NOT NULL DEFAULT 0,
  `created_date` datetime NOT NULL DEFAULT current_timestamp(),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

-- The account, status page and maintenance tables below were created by running the knex init and
-- migrations of the Node.js backend at 921745bf^ on MariaDB 11, and dumped with SHOW CREATE TABLE.

CREATE TABLE `user` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `username` varchar(255) CHARACTER SET utf8mb3 COLLATE utf8mb3_general_ci NOT NULL,
  `password` varchar(255) DEFAULT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `timezone` varchar(150) DEFAULT NULL,
  `twofa_secret` varchar(64) DEFAULT NULL,
  `twofa_status` tinyint(1) NOT NULL DEFAULT 0,
  `twofa_last_token` varchar(6) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `user_username_unique` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `better_auth_user` (
  `id` varchar(255) NOT NULL,
  `name` varchar(255) NOT NULL,
  `email` varchar(255) NOT NULL,
  `emailVerified` tinyint(1) NOT NULL,
  `image` varchar(255) DEFAULT NULL,
  `createdAt` timestamp NOT NULL,
  `updatedAt` timestamp NOT NULL,
  `username` varchar(255) DEFAULT NULL,
  `displayUsername` varchar(255) DEFAULT NULL,
  `role` text DEFAULT NULL,
  `banned` tinyint(1) DEFAULT NULL,
  `banReason` text DEFAULT NULL,
  `banExpires` timestamp NULL DEFAULT NULL,
  `twoFactorEnabled` tinyint(1) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `better_auth_user_username_unique` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `better_auth_account` (
  `id` varchar(255) NOT NULL,
  `userId` varchar(255) NOT NULL,
  `accountId` varchar(255) NOT NULL,
  `providerId` varchar(255) NOT NULL,
  `accessToken` varchar(255) DEFAULT NULL,
  `refreshToken` varchar(255) DEFAULT NULL,
  `accessTokenExpiresAt` timestamp NULL DEFAULT NULL,
  `refreshTokenExpiresAt` timestamp NULL DEFAULT NULL,
  `scope` varchar(255) DEFAULT NULL,
  `idToken` varchar(255) DEFAULT NULL,
  `password` varchar(255) DEFAULT NULL,
  `createdAt` timestamp NOT NULL,
  `updatedAt` timestamp NOT NULL,
  PRIMARY KEY (`id`),
  KEY `better_auth_account_userid_foreign` (`userId`),
  CONSTRAINT `better_auth_account_userid_foreign` FOREIGN KEY (`userId`) REFERENCES `better_auth_user` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `better_auth_twoFactor` (
  `id` varchar(255) NOT NULL,
  `secret` varchar(255) NOT NULL,
  `backupCodes` varchar(255) NOT NULL,
  `verified` tinyint(1) NOT NULL,
  `userId` varchar(255) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `better_auth_twofactor_userid_foreign` (`userId`),
  CONSTRAINT `better_auth_twofactor_userid_foreign` FOREIGN KEY (`userId`) REFERENCES `better_auth_user` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `setting` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `key` varchar(200) CHARACTER SET utf8mb3 COLLATE utf8mb3_general_ci NOT NULL,
  `value` text DEFAULT NULL,
  `type` varchar(20) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `setting_key_unique` (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `status_page` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `slug` varchar(255) CHARACTER SET utf8mb3 COLLATE utf8mb3_general_ci NOT NULL,
  `title` varchar(255) NOT NULL,
  `description` text DEFAULT NULL,
  `icon` varchar(255) NOT NULL,
  `theme` varchar(30) NOT NULL,
  `published` tinyint(1) NOT NULL DEFAULT 1,
  `search_engine_index` tinyint(1) NOT NULL DEFAULT 1,
  `show_tags` tinyint(1) NOT NULL DEFAULT 0,
  `password` varchar(255) DEFAULT NULL,
  `created_date` datetime NOT NULL DEFAULT current_timestamp(),
  `modified_date` datetime NOT NULL DEFAULT current_timestamp(),
  `footer_text` text DEFAULT NULL,
  `custom_css` text DEFAULT NULL,
  `show_powered_by` tinyint(1) NOT NULL DEFAULT 1,
  `analytics_id` varchar(255) DEFAULT NULL,
  `show_certificate_expiry` tinyint(1) NOT NULL DEFAULT 0,
  `auto_refresh_interval` int(10) unsigned DEFAULT 300,
  `analytics_script_url` varchar(255) DEFAULT NULL,
  `analytics_type` varchar(255) DEFAULT NULL,
  `show_only_last_heartbeat` tinyint(1) NOT NULL DEFAULT 0,
  `rss_title` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `status_page_slug_unique` (`slug`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `status_page_cname` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `status_page_id` int(10) unsigned DEFAULT NULL,
  `domain` varchar(255) CHARACTER SET utf8mb3 COLLATE utf8mb3_general_ci NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `status_page_cname_domain_unique` (`domain`),
  KEY `status_page_cname_status_page_id_foreign` (`status_page_id`),
  CONSTRAINT `status_page_cname_status_page_id_foreign` FOREIGN KEY (`status_page_id`) REFERENCES `status_page` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `group` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL,
  `created_date` datetime NOT NULL DEFAULT current_timestamp(),
  `public` tinyint(1) NOT NULL DEFAULT 0,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `weight` int(11) NOT NULL DEFAULT 1000,
  `status_page_id` int(10) unsigned DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `monitor_group` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` int(10) unsigned NOT NULL,
  `group_id` int(10) unsigned NOT NULL,
  `weight` int(11) NOT NULL DEFAULT 1000,
  `send_url` tinyint(1) NOT NULL DEFAULT 0,
  `custom_url` text DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `monitor_group_group_id_foreign` (`group_id`),
  KEY `fk` (`monitor_id`,`group_id`),
  CONSTRAINT `monitor_group_group_id_foreign` FOREIGN KEY (`group_id`) REFERENCES `group` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `monitor_group_monitor_id_foreign` FOREIGN KEY (`monitor_id`) REFERENCES `monitor` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `incident` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `title` varchar(255) NOT NULL,
  `content` text NOT NULL,
  `style` varchar(30) NOT NULL DEFAULT 'warning',
  `created_date` datetime NOT NULL DEFAULT current_timestamp(),
  `last_updated_date` datetime DEFAULT NULL,
  `pin` tinyint(1) NOT NULL DEFAULT 1,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `status_page_id` int(10) unsigned DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `maintenance` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `title` varchar(150) NOT NULL,
  `description` text NOT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `strategy` varchar(50) NOT NULL DEFAULT 'single',
  `start_date` datetime DEFAULT NULL,
  `end_date` datetime DEFAULT NULL,
  `start_time` time DEFAULT NULL,
  `end_time` time DEFAULT NULL,
  `weekdays` varchar(250) DEFAULT '[]',
  `days_of_month` text DEFAULT NULL,
  `interval_day` int(11) DEFAULT NULL,
  `cron` text DEFAULT NULL,
  `timezone` varchar(255) DEFAULT NULL,
  `duration` int(11) DEFAULT NULL,
  `last_start_date` datetime DEFAULT NULL,
  `user_id` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `maintenance_active_index` (`active`),
  KEY `manual_active` (`strategy`,`active`),
  KEY `maintenance_user_id_foreign` (`user_id`),
  CONSTRAINT `maintenance_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `better_auth_user` (`id`) ON DELETE SET NULL ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `monitor_maintenance` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` int(10) unsigned NOT NULL,
  `maintenance_id` int(10) unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `maintenance_id_index2` (`maintenance_id`),
  KEY `monitor_id_index` (`monitor_id`),
  CONSTRAINT `monitor_maintenance_maintenance_id_foreign` FOREIGN KEY (`maintenance_id`) REFERENCES `maintenance` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `monitor_maintenance_monitor_id_foreign` FOREIGN KEY (`monitor_id`) REFERENCES `monitor` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;

CREATE TABLE `maintenance_status_page` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `status_page_id` int(10) unsigned NOT NULL,
  `maintenance_id` int(10) unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `maintenance_status_page_maintenance_id_foreign` (`maintenance_id`),
  KEY `maintenance_status_page_status_page_id_foreign` (`status_page_id`),
  CONSTRAINT `maintenance_status_page_maintenance_id_foreign` FOREIGN KEY (`maintenance_id`) REFERENCES `maintenance` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `maintenance_status_page_status_page_id_foreign` FOREIGN KEY (`status_page_id`) REFERENCES `status_page` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;
