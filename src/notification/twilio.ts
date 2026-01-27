import { NotificationService } from './interface';

/**
 * Twilio SMS implementation of NotificationService
 * Requires TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_FROM_NUMBER, and TWILIO_TO_NUMBER in env
 */
export class TwilioNotification implements NotificationService {
  private accountSid: string;
  private authToken: string;
  private fromNumber: string;
  private toNumber: string;

  constructor() {
    this.accountSid = process.env.TWILIO_ACCOUNT_SID ?? '';
    this.authToken = process.env.TWILIO_AUTH_TOKEN ?? '';
    this.fromNumber = process.env.TWILIO_FROM_NUMBER ?? '';
    this.toNumber = process.env.TWILIO_TO_NUMBER ?? '';

    if (!this.accountSid || !this.authToken || !this.fromNumber || !this.toNumber) {
      throw new Error('Twilio configuration incomplete. Set TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_FROM_NUMBER, and TWILIO_TO_NUMBER');
    }
  }

  async sendWarning(message: string): Promise<void> {
    await this.sendSMS(`[WARNING] ${message}`);
  }

  async sendError(message: string): Promise<void> {
    await this.sendSMS(`[ERROR] ${message}`);
  }

  async sendInfo(message: string): Promise<void> {
    await this.sendSMS(`[INFO] ${message}`);
  }

  private async sendSMS(message: string): Promise<void> {
    try {
      const response = await fetch(
        `https://api.twilio.com/2010-04-01/Accounts/${this.accountSid}/Messages.json`,
        {
          method: 'POST',
          headers: {
            'Authorization': 'Basic ' + Buffer.from(`${this.accountSid}:${this.authToken}`).toString('base64'),
            'Content-Type': 'application/x-www-form-urlencoded',
          },
          body: new URLSearchParams({
            From: this.fromNumber,
            To: this.toNumber,
            Body: message,
          }),
        }
      );

      if (!response.ok) {
        throw new Error(`Twilio API error: ${response.statusText}`);
      }
    } catch (error) {
      console.error('Failed to send Twilio SMS:', error);
      // Fallback to console logging
      console.log(message);
    }
  }
}
