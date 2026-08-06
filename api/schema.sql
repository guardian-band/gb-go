CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    phone_number VARCHAR(32) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS patient_profiles (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    birth_date DATE,
    blood_type VARCHAR(8),
    critical_facts JSONB NOT NULL DEFAULT '{}'::JSONB,
    timezone VARCHAR(64) NOT NULL DEFAULT 'UTC',
    avatar_object_key TEXT,
    band_identifier VARCHAR(128) UNIQUE,
    qr_token_hash TEXT UNIQUE,
    band_last_seen_at TIMESTAMP WITH TIME ZONE,
    last_latitude DECIMAL(9, 6),
    last_longitude DECIMAL(9, 6),
    last_location_accuracy DECIMAL(10, 2),
    last_location_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT patient_profiles_latitude_valid
        CHECK (last_latitude IS NULL OR last_latitude BETWEEN -90 AND 90),
    CONSTRAINT patient_profiles_longitude_valid
        CHECK (last_longitude IS NULL OR last_longitude BETWEEN -180 AND 180),
    CONSTRAINT patient_profiles_accuracy_valid
        CHECK (last_location_accuracy IS NULL OR last_location_accuracy >= 0)
);

CREATE TABLE IF NOT EXISTS patient_links (
    id UUID PRIMARY KEY,
    patient_id UUID NOT NULL REFERENCES patient_profiles(user_id) ON DELETE CASCADE,
    linked_user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    display_name VARCHAR(200),
    phone VARCHAR(32),
    relationship VARCHAR(64) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    can_monitor BOOLEAN NOT NULL DEFAULT FALSE,
    is_emergency_contact BOOLEAN NOT NULL DEFAULT FALSE,
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    CONSTRAINT patient_links_kind_valid
        CHECK (kind IN ('family', 'clinician', 'other')),
    CONSTRAINT patient_links_identity_present
        CHECK (
            linked_user_id IS NOT NULL
            OR (display_name IS NOT NULL AND phone IS NOT NULL)
        ),
    CONSTRAINT patient_links_not_self
        CHECK (linked_user_id IS NULL OR linked_user_id <> patient_id)
);

CREATE TABLE IF NOT EXISTS documents (
    id UUID PRIMARY KEY,
    patient_id UUID NOT NULL REFERENCES patient_profiles(user_id) ON DELETE CASCADE,
    kind VARCHAR(32) NOT NULL,
    title VARCHAR(300) NOT NULL,
    object_key TEXT NOT NULL UNIQUE,
    current_version_id TEXT NOT NULL,
    etag TEXT,
    content_type VARCHAR(255) NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT documents_kind_valid
        CHECK (kind IN ('prescription', 'report'))
);

CREATE TABLE IF NOT EXISTS medications (
    id UUID PRIMARY KEY,
    patient_id UUID NOT NULL REFERENCES patient_profiles(user_id) ON DELETE CASCADE,
    prescription_document_id UUID REFERENCES documents(id) ON DELETE SET NULL,
    name VARCHAR(200) NOT NULL,
    strength VARCHAR(100),
    instructions TEXT,
    schedule JSONB NOT NULL DEFAULT '{}'::JSONB,
    active BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE IF NOT EXISTS medication_events (
    id UUID PRIMARY KEY,
    medication_id UUID NOT NULL REFERENCES medications(id) ON DELETE CASCADE,
    scheduled_for TIMESTAMP WITH TIME ZONE NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    taken_at TIMESTAMP WITH TIME ZONE,
    confirmed_by UUID REFERENCES users(id),
    CONSTRAINT medication_events_status_valid
        CHECK (status IN ('pending', 'taken', 'missed', 'skipped')),
    CONSTRAINT medication_events_taken_state_valid
        CHECK (
            (status = 'taken' AND taken_at IS NOT NULL)
            OR (status <> 'taken' AND taken_at IS NULL)
        ),
    CONSTRAINT medication_events_occurrence_unique
        UNIQUE (medication_id, scheduled_for)
);

CREATE TABLE IF NOT EXISTS health_state (
    patient_id UUID NOT NULL REFERENCES patient_profiles(user_id) ON DELETE CASCADE,
    metric_type VARCHAR(64) NOT NULL,
    value_type VARCHAR(16) NOT NULL,
    number_value DECIMAL,
    boolean_value BOOLEAN,
    text_value TEXT,
    unit VARCHAR(32),
    observed_at TIMESTAMP WITH TIME ZONE NOT NULL,
    PRIMARY KEY (patient_id, metric_type),
    CONSTRAINT health_state_value_type_valid
        CHECK (value_type IN ('number', 'boolean', 'text')),
    CONSTRAINT health_state_value_matches_type
        CHECK (
            (
                value_type = 'number'
                AND number_value IS NOT NULL
                AND boolean_value IS NULL
                AND text_value IS NULL
            )
            OR (
                value_type = 'boolean'
                AND number_value IS NULL
                AND boolean_value IS NOT NULL
                AND text_value IS NULL
            )
            OR (
                value_type = 'text'
                AND number_value IS NULL
                AND boolean_value IS NULL
                AND text_value IS NOT NULL
            )
        )
);

CREATE TABLE IF NOT EXISTS sos_incidents (
    id UUID PRIMARY KEY,
    patient_id UUID NOT NULL REFERENCES patient_profiles(user_id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    latitude DECIMAL(9, 6),
    longitude DECIMAL(9, 6),
    accuracy_meters DECIMAL(10, 2),
    address TEXT,
    started_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMP WITH TIME ZONE,
    cancelled_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT sos_incidents_status_valid
        CHECK (status IN ('active', 'responding', 'resolved', 'cancelled')),
    CONSTRAINT sos_incidents_latitude_valid
        CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
    CONSTRAINT sos_incidents_longitude_valid
        CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180),
    CONSTRAINT sos_incidents_accuracy_valid
        CHECK (accuracy_meters IS NULL OR accuracy_meters >= 0),
    CONSTRAINT sos_incidents_completion_valid
        CHECK (
            (status = 'resolved' AND resolved_at IS NOT NULL AND cancelled_at IS NULL)
            OR (status = 'cancelled' AND cancelled_at IS NOT NULL AND resolved_at IS NULL)
            OR (
                status IN ('active', 'responding')
                AND resolved_at IS NULL
                AND cancelled_at IS NULL
            )
        )
);

CREATE TABLE IF NOT EXISTS emergency_access_sessions (
    id UUID PRIMARY KEY,
    patient_id UUID NOT NULL REFERENCES patient_profiles(user_id) ON DELETE CASCADE,
    responder_id UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    revoked_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT emergency_access_sessions_expiry_valid
        CHECK (expires_at > created_at),
    CONSTRAINT emergency_access_sessions_revocation_valid
        CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE TABLE IF NOT EXISTS medication_catalog (
    id UUID PRIMARY KEY,
    name VARCHAR(200) NOT NULL,
    strength VARCHAR(100) NOT NULL
);
