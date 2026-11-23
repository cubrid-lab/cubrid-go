package cubrid

// tx implements database/sql/driver.Tx.
type tx struct {
	conn *conn
}

// Commit commits the current transaction.
func (t *tx) Commit() error {
	t.conn.mu.Lock()
	defer t.conn.mu.Unlock()

	req := WriteEndTran(TxCommit, t.conn.casInfo)
	resp, err := t.conn.sendAndRecv(req)
	if err != nil {
		return err
	}
	if err = ParseSimpleResponse(resp); err != nil {
		return t.conn.replyErr(err)
	}
	t.conn.autoCommit = true
	return nil
}

// Rollback aborts the current transaction.
func (t *tx) Rollback() error {
	t.conn.mu.Lock()
	defer t.conn.mu.Unlock()

	req := WriteEndTran(TxRollback, t.conn.casInfo)
	resp, err := t.conn.sendAndRecv(req)
	if err != nil {
		return err
	}
	if err = ParseSimpleResponse(resp); err != nil {
		return t.conn.replyErr(err)
	}
	t.conn.autoCommit = true
	return nil
}
