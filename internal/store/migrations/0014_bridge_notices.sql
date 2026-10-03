-- Bridges' notices: the dashboard follows a bridge as its settings, its account, its plugin's switch, and its rounds
-- change (docs/design/08-dashboard-and-api.md#live-updates). What a bridge remembers between rounds, and the tokens
-- its plugin renews, change nothing the dashboard shows, and write none.
ALTER TABLE event_notice DROP CONSTRAINT event_notice_kind_check;
ALTER TABLE event_notice ADD CONSTRAINT event_notice_kind_check CHECK (kind IN ('verdict', 'task', 'group', 'pr', 'proposal', 'bridge'));

CREATE CONSTRAINT TRIGGER bridge_created_notice AFTER INSERT ON bridge DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('bridge', 'id');
CREATE CONSTRAINT TRIGGER bridge_notice AFTER UPDATE ON bridge DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN ((OLD.name, OLD.link_template, OLD.plugin, OLD.enabled, OLD.credentials, OLD.chat, OLD.chat_name, OLD.every_minutes,
           OLD.lookback_days, OLD.propose_from, OLD.model_url, OLD.model_name, OLD.model_key, OLD.model_timeout_seconds)
          IS DISTINCT FROM
          (NEW.name, NEW.link_template, NEW.plugin, NEW.enabled, NEW.credentials, NEW.chat, NEW.chat_name, NEW.every_minutes,
           NEW.lookback_days, NEW.propose_from, NEW.model_url, NEW.model_name, NEW.model_key, NEW.model_timeout_seconds))
    EXECUTE FUNCTION notice('bridge', 'id');

CREATE CONSTRAINT TRIGGER bridge_account_created_notice AFTER INSERT ON bridge_account DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('bridge', 'bridge_id');
CREATE CONSTRAINT TRIGGER bridge_account_notice AFTER UPDATE ON bridge_account DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN ((OLD.subject, OLD.name, OLD.connected_by, OLD.connected_at, OLD.lapsed_at)
          IS DISTINCT FROM (NEW.subject, NEW.name, NEW.connected_by, NEW.connected_at, NEW.lapsed_at))
    EXECUTE FUNCTION notice('bridge', 'bridge_id');

CREATE CONSTRAINT TRIGGER bridge_round_notice AFTER INSERT ON bridge_round DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('bridge', 'bridge_id');

-- A plugin's switch names the plugin's kind as its subject: the bridges it runs change with it.
CREATE CONSTRAINT TRIGGER bridge_plugin_notice AFTER INSERT OR UPDATE ON bridge_plugin DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('bridge', 'kind');
